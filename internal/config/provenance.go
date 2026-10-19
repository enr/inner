package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ProfileLayer is one file of a profile's extends chain.
type ProfileLayer struct {
	// Label names the layer in an explanation: the profile name, or the file
	// path when two layers of the chain share a name (a local profile that
	// extends the global one of the same name).
	Label string
	Path  string
}

// ExplainedValue is one value of the merged profile together with the layer
// it comes from.
//
// Scalars produce one ExplainedValue each. Lists and maps produce one per
// element: Item is the list element or the map key, Value is empty for a list
// element and the mapped value for a map entry.
type ExplainedValue struct {
	Key   string // dotted TOML key, e.g. "sandbox.home", "mounts", "env.set"
	Item  string
	Value string
	// Layer is the Label of the layer that set the value, or "" when no layer
	// sets it and the value is the built-in default.
	Layer string
}

// ProfileExplanation is the effective policy of a profile: the merged result
// of its extends chain, with every value attributed to the layer that
// contributed it.
type ProfileExplanation struct {
	// Name is the name of the requested profile (the last layer), the same as
	// Profile.Name.
	Name    string
	Profile *Profile       // the merged profile, exactly what LoadProfileAuto returns
	Layers  []ProfileLayer // base first, the requested profile last
	Values  []ExplainedValue
}

// replacedLists are the list keys that an overlay replaces instead of
// unioning (see mergeProfiles): the whole list belongs to the last layer that
// sets it.
var replacedLists = map[string]bool{
	"entrypoint.args": true,
}

// skippedKeys are not part of the effective policy: extends is the chain
// itself, shown as Layers, and name labels the layers.
var skippedKeys = map[string]bool{
	"extends": true,
	"name":    true,
}

// ExplainProfile resolves nameOrPath like LoadProfileAuto and returns the
// merged profile with the provenance of each of its values.
func (l *Loader) ExplainProfile(nameOrPath string) (*ProfileExplanation, error) {
	path, err := l.autoProfilePath(nameOrPath)
	if err != nil {
		return nil, err
	}
	chain, err := l.loadProfileChain(path, nil)
	if err != nil {
		return nil, fmt.Errorf("reading profile %q: %w", nameOrPath, err)
	}
	merged := mergeChain(chain)

	layers := layerLabels(chain)
	o := newOrigins()
	for i, layer := range chain {
		o.record(layer, layers[i].Label)
	}
	return &ProfileExplanation{
		Name:    chain[len(chain)-1].profile.Name,
		Profile: merged,
		Layers:  layers,
		Values:  o.explain(merged),
	}, nil
}

// autoProfilePath returns the file LoadProfileAuto would load for nameOrPath.
func (l *Loader) autoProfilePath(nameOrPath string) (string, error) {
	absOr := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	expanded := ExpandPath(nameOrPath)
	if _, err := os.Stat(expanded); err == nil {
		return absOr(expanded), nil
	}
	if strings.ContainsAny(expanded, "/\\") {
		if l.WorkDir != "" {
			candidate := filepath.Join(l.WorkDir, expanded)
			if _, err := os.Stat(candidate); err == nil {
				return absOr(candidate), nil
			}
		}
		return "", fmt.Errorf("profile file not found: %s", nameOrPath)
	}
	if err := ValidateProfileName(nameOrPath); err != nil {
		return "", err
	}
	if localPath := l.LocalProfilePath(nameOrPath); localPath != "" {
		if _, err := os.Stat(localPath); err == nil {
			return absOr(localPath), nil
		}
	}
	path := l.ProfilePath(nameOrPath)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("profile %q not found at %s", nameOrPath, path)
	}
	return absOr(path), nil
}

// layerLabels names each layer by its profile name, falling back to the path
// for names that appear more than once in the chain.
func layerLabels(chain []profileLayer) []ProfileLayer {
	count := map[string]int{}
	for _, layer := range chain {
		count[layer.profile.Name]++
	}
	out := make([]ProfileLayer, len(chain))
	for i, layer := range chain {
		label := layer.profile.Name
		if count[label] > 1 {
			label = layer.path
		}
		out[i] = ProfileLayer{Label: label, Path: layer.path}
	}
	return out
}

// origins accumulates, layer by layer, which layer set each value. It mirrors
// the rules of mergeProfiles:
//
//   - scalars and replaced lists: the last layer that defines the key;
//   - unioned lists: the first layer that introduces the element;
//   - maps: the last layer that sets the map key;
//   - verify.custom.checks: appended, so each layer owns the checks it lists.
type origins struct {
	scalar map[string]string            // key → layer
	item   map[string]map[string]string // key → element or map key → layer
	checks []string                     // layer of each merged custom check, in order
}

func newOrigins() *origins {
	return &origins{scalar: map[string]string{}, item: map[string]map[string]string{}}
}

func (o *origins) setItem(key, item, layer string, overwrite bool) {
	m := o.item[key]
	if m == nil {
		m = map[string]string{}
		o.item[key] = m
	}
	if _, seen := m[item]; seen && !overwrite {
		return
	}
	m[item] = layer
}

func (o *origins) record(layer profileLayer, label string) {
	defined := func(path []string) bool { return layer.meta.IsDefined(path...) }
	walkProfile(reflect.ValueOf(layer.profile).Elem(), nil, func(path []string, v reflect.Value) {
		key := strings.Join(path, ".")
		switch {
		case v.Kind() == reflect.Map:
			// mergeProfiles merges every key the overlay's map carries.
			for _, k := range v.MapKeys() {
				o.setItem(key, k.String(), label, true)
			}
		case key == "verify.custom.checks":
			if defined(path) {
				for i := 0; i < v.Len(); i++ {
					o.checks = append(o.checks, label)
				}
			}
		case v.Kind() == reflect.Slice && !replacedLists[key]:
			if defined(path) {
				for i := 0; i < v.Len(); i++ {
					o.setItem(key, v.Index(i).String(), label, false)
				}
			}
		default:
			if defined(path) {
				o.scalar[key] = label
			}
		}
	})
	// network and network_mode are one decision: whichever of the two a layer
	// sets, mergeProfiles rewrites both.
	if defined([]string{"sandbox", "network"}) || defined([]string{"sandbox", "network_mode"}) {
		o.scalar["sandbox.network"] = label
		o.scalar["sandbox.network_mode"] = label
	}
}

// explain lists every value of the merged profile with its origin. Values that
// no layer sets are listed only when they differ from the zero value (a
// built-in default worth knowing about, such as the resolved network_mode).
func (o *origins) explain(merged *Profile) []ExplainedValue {
	var out []ExplainedValue
	walkProfile(reflect.ValueOf(merged).Elem(), nil, func(path []string, v reflect.Value) {
		key := strings.Join(path, ".")
		switch {
		case v.Kind() == reflect.Map:
			keys := make([]string, 0, v.Len())
			for _, k := range v.MapKeys() {
				keys = append(keys, k.String())
			}
			sort.Strings(keys)
			for _, k := range keys {
				out = append(out, ExplainedValue{
					Key: key, Item: k,
					Value: formatValue(v.MapIndex(reflect.ValueOf(k))),
					Layer: o.item[key][k],
				})
			}
		case key == "verify.custom.checks":
			for i := 0; i < v.Len(); i++ {
				c := v.Index(i).Interface().(CustomCheck)
				layer := ""
				if i < len(o.checks) {
					layer = o.checks[i]
				}
				out = append(out, ExplainedValue{Key: key, Item: c.Name, Value: c.Cmd, Layer: layer})
			}
		case v.Kind() == reflect.Slice && !replacedLists[key]:
			for i := 0; i < v.Len(); i++ {
				item := v.Index(i).String()
				out = append(out, ExplainedValue{Key: key, Item: item, Layer: o.item[key][item]})
			}
		default:
			layer := o.scalar[key]
			if layer == "" && v.IsZero() {
				return
			}
			out = append(out, ExplainedValue{Key: key, Value: formatValue(v), Layer: layer})
		}
	})
	return out
}

// walkProfile calls visit for every leaf field of a profile struct, named by
// its TOML key path. Nested structs are descended into; nil pointers are
// skipped; maps, slices and scalars are leaves.
func walkProfile(v reflect.Value, prefix []string, visit func(path []string, v reflect.Value)) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := append(append([]string(nil), prefix...), name)
		if skippedKeys[strings.Join(path, ".")] {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		if fv.Kind() == reflect.Struct {
			walkProfile(fv, path, visit)
			continue
		}
		visit(path, fv)
	}
}

// formatValue renders a leaf value the way it reads in TOML.
func formatValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Slice:
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = formatValue(v.Index(i))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Struct:
		if m, ok := v.Interface().(MountEntry); ok {
			mode := m.Mode
			if mode == "" {
				mode = "ro"
			}
			if m.GitScan != nil && !*m.GitScan {
				mode += ", git_scan = false"
			}
			if m.Dest == "" {
				return mode
			}
			return m.Dest + " (" + mode + ")"
		}
		return fmt.Sprintf("%+v", v.Interface())
	default:
		return fmt.Sprint(v.Interface())
	}
}
