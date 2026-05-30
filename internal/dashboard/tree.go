package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// namespaceFor splits a package name into (namespace, leaf) per ecosystem.
// Flat ecosystems (pypi, cargo) and unscoped names return ("", name).
func namespaceFor(eco, name string) (ns, leaf string) {
	switch eco {
	case "npm":
		if strings.HasPrefix(name, "@") {
			if i := strings.IndexByte(name, '/'); i > 0 {
				return name[:i], name[i+1:]
			}
		}
	case "maven":
		if i := strings.IndexByte(name, ':'); i > 0 {
			return name[:i], name[i+1:]
		}
	case "go":
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			return name[:i], name[i+1:]
		}
	case "nuget":
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			return name[:i], name[i+1:]
		}
	}
	return "", name
}

// TreeVersion is a leaf: a specific package version with its status & metadata.
type TreeVersion struct {
	Version  string    `json:"version"`
	Action   string    `json:"action"`
	Signal   string    `json:"signal"`
	Reason   string    `json:"reason"`
	Size     int64     `json:"size"` // -1 when unknown
	Cached   bool      `json:"cached"`
	CVECount int       `json:"cve_count"`
	LastSeen time.Time `json:"last_seen"`
	HitCount int       `json:"hit_count"`
}

type TreePackage struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Versions  []TreeVersion `json:"versions"`
}

type TreeEcosystem struct {
	Ecosystem string        `json:"ecosystem"`
	Packages  []TreePackage `json:"packages"`
}

// handlePackagesTree returns the ecosystem→namespace/package→version hierarchy.
func (d *Dashboard) handlePackagesTree(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	all := d.log.Events("") // newest-first

	type vkey struct{ eco, name, version string }
	type pkey struct{ eco, ns, name string }
	verSeen := map[vkey]*TreeVersion{}
	pkgVers := map[pkey][]*TreeVersion{}
	pkgOrder := []pkey{}

	for _, e := range all {
		if eco != "" && e.Ecosystem != eco {
			continue
		}
		name, version := splitPackage(e.Package)
		vk := vkey{e.Ecosystem, name, version}
		if v, ok := verSeen[vk]; ok {
			v.HitCount++
			continue
		}
		ns, leaf := namespaceFor(e.Ecosystem, name)
		tv := &TreeVersion{
			Version: version, Action: e.Action, Signal: e.Signal, Reason: e.Reason,
			Size: -1, CVECount: len(e.Vulns), LastSeen: e.Timestamp, HitCount: 1,
		}
		if d.cache != nil {
			tv.Cached = blobCached(r.Context(), d.cache, e.Ecosystem, name, version)
			if tv.Cached {
				if sz := blobSize(r.Context(), d.cache, e.Ecosystem, name, version); sz >= 0 {
					tv.Size = sz
				}
			}
		}
		verSeen[vk] = tv
		pk := pkey{e.Ecosystem, ns, leaf}
		if _, ok := pkgVers[pk]; !ok {
			pkgOrder = append(pkgOrder, pk)
		}
		pkgVers[pk] = append(pkgVers[pk], tv)
	}

	// Group packages by ecosystem.
	ecoIdx := map[string]int{}
	var out []TreeEcosystem
	for _, pk := range pkgOrder {
		i, ok := ecoIdx[pk.eco]
		if !ok {
			i = len(out)
			ecoIdx[pk.eco] = i
			out = append(out, TreeEcosystem{Ecosystem: pk.eco})
		}
		vers := make([]TreeVersion, 0, len(pkgVers[pk]))
		for _, v := range pkgVers[pk] {
			vers = append(vers, *v)
		}
		sort.Slice(vers, func(a, b int) bool { return vers[a].Version > vers[b].Version })
		out[i].Packages = append(out[i].Packages, TreePackage{Namespace: pk.ns, Name: pk.name, Versions: vers})
	}
	for i := range out {
		sort.Slice(out[i].Packages, func(a, b int) bool {
			pa, pb := out[i].Packages[a], out[i].Packages[b]
			if pa.Namespace != pb.Namespace {
				return pa.Namespace < pb.Namespace
			}
			return pa.Name < pb.Name
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Ecosystem < out[b].Ecosystem })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
