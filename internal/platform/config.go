package platform

import "sort"

var registry = map[string]PlatformDriver{}

func Register(d PlatformDriver) {
	registry[d.Name()] = d
}

func Get(name string) (PlatformDriver, bool) {
	d, ok := registry[name]
	return d, ok
}

func All() []PlatformDriver {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]PlatformDriver, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
