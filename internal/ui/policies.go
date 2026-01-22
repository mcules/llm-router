package ui

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mcules/llm-router/internal/policy"
)

type PolicyViewRow struct {
	ModelID          string
	RAMRequiredBytes uint64
	TTLSecs          int
	Priority         int
	Pinned           bool
}

func (h *Handler) policies(w http.ResponseWriter, r *http.Request) {
	rows := make([]PolicyViewRow, 0, 128)

	if h.PolicyStore != nil {
		// Call PolicyStore.ListAll(ctx) via reflection.
		res, err := callListAll(r.Context(), h.PolicyStore)
		if err == nil {
			rows = append(rows, res...)
		}
	}

	vm := viewModel{
		Now:      time.Now(),
		Nodes:    h.Cluster.Snapshot(),
		Policies: rows,
	}
	h.render(w, "policies.html", vm)
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	modelID := r.FormValue("model_id")
	if modelID != "" {
		_ = h.PolicyStore.Delete(r.Context(), modelID)
	}
	http.Redirect(w, r, "/ui/policies", http.StatusFound)
}

func (h *Handler) upsertPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	modelID := r.FormValue("model_id")
	if modelID == "" {
		http.Error(w, "missing model_id", http.StatusBadRequest)
		return
	}

	// Fetch existing or start new
	p, _, _ := h.PolicyStore.GetPolicy(r.Context(), modelID)

	if r.FormValue("ram_required_bytes") != "" {
		p.RAMRequiredBytes = parseUint64Default(r.FormValue("ram_required_bytes"), p.RAMRequiredBytes)
	}
	if r.FormValue("ttl_secs") != "" {
		p.TTLSecs = int64(parseIntDefault(r.FormValue("ttl_secs"), int(p.TTLSecs)))
	}
	if r.FormValue("priority") != "" {
		p.Priority = parseIntDefault(r.FormValue("priority"), p.Priority)
	}
	if r.FormValue("pinned") != "" {
		p.Pinned = r.FormValue("pinned") == "true"
	}

	_ = h.PolicyStore.Upsert(r.Context(), p)

	http.Redirect(w, r, r.Referer(), http.StatusFound)
}

func (h *Handler) savePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	modelID := r.FormValue("model_id")
	ram := parseUint64Default(r.FormValue("ram_required_bytes"), 0)
	ttl := parseIntDefault(r.FormValue("ttl_secs"), 0)
	prio := parseIntDefault(r.FormValue("priority"), 0)
	pinned := r.FormValue("pinned") != ""

	if modelID == "" {
		http.Error(w, "model_id is required", http.StatusBadRequest)
		return
	}

	err := h.PolicyStore.Upsert(r.Context(), policy.ModelPolicy{
		ModelID:          modelID,
		RAMRequiredBytes: ram,
		TTLSecs:          int64(ttl),
		Priority:         prio,
		Pinned:           pinned,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to save policy: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/policies", http.StatusFound)
}

func parseIntDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseUint64Default(s string, def uint64) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// --- Reflection helpers ---

func callListAll(ctx context.Context, store any) ([]PolicyViewRow, error) {
	v := reflect.ValueOf(store)
	m := v.MethodByName("ListAll")
	if !m.IsValid() {
		return nil, errString("PolicyStore missing method ListAll(ctx)")
	}

	out := m.Call([]reflect.Value{reflect.ValueOf(ctx)})
	if len(out) != 2 {
		return nil, errString("PolicyStore.ListAll(ctx) must return (slice, error)")
	}

	if !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}

	sliceVal := out[0]
	if sliceVal.Kind() != reflect.Slice && sliceVal.Kind() != reflect.Array {
		return nil, errString("PolicyStore.ListAll(ctx) first return value must be a slice")
	}

	rows := make([]PolicyViewRow, 0, sliceVal.Len())
	for i := 0; i < sliceVal.Len(); i++ {
		p := sliceVal.Index(i)
		rows = append(rows, policyToRow(p))
	}

	return rows, nil
}

func callUpsert(ctx context.Context, store any, modelID string, ram uint64, ttl int, prio int, pinned bool) error {
	v := reflect.ValueOf(store)
	m := v.MethodByName("Upsert")
	if !m.IsValid() {
		return errString("PolicyStore missing method Upsert(ctx, policy)")
	}

	mt := m.Type()
	if mt.NumIn() != 2 {
		return errString("PolicyStore.Upsert must have signature Upsert(ctx, policy)")
	}

	policyType := mt.In(1)

	// Create a new policy value.
	var pv reflect.Value
	switch policyType.Kind() {
	case reflect.Pointer:
		pv = reflect.New(policyType.Elem())
	default:
		pv = reflect.New(policyType).Elem()
	}

	// Set fields with multiple candidate names.
	setStringField(pv, []string{"ModelID", "ModelId", "model_id", "modelID", "id", "ID"}, modelID)
	setUintField(pv, []string{"RAMRequiredBytes", "RamRequiredBytes", "ram_required_bytes", "ramRequiredBytes"}, ram)
	setIntField(pv, []string{"TTLSecs", "TtlSecs", "ttl_secs", "ttlSeconds", "TTLSeconds"}, ttl)
	setIntField(pv, []string{"Priority", "priority"}, prio)
	setBoolField(pv, []string{"Pinned", "pinned"}, pinned)

	// Call Upsert(ctx, policyValue)
	argPolicy := pv
	if policyType.Kind() == reflect.Pointer {
		if pv.Kind() != reflect.Pointer {
			// pv is non-pointer, make pointer
			tmp := reflect.New(policyType.Elem())
			tmp.Elem().Set(pv)
			argPolicy = tmp
		}
	} else {
		if pv.Kind() == reflect.Pointer {
			argPolicy = pv.Elem()
		}
	}

	out := m.Call([]reflect.Value{reflect.ValueOf(ctx), argPolicy})
	if len(out) != 1 {
		return errString("PolicyStore.Upsert must return (error)")
	}
	if out[0].IsNil() {
		return nil
	}
	return out[0].Interface().(error)
}

func policyToRow(p reflect.Value) PolicyViewRow {
	// Dereference pointers.
	for p.Kind() == reflect.Pointer {
		if p.IsNil() {
			return PolicyViewRow{}
		}
		p = p.Elem()
	}

	row := PolicyViewRow{
		ModelID:          getStringField(p, []string{"ModelID", "ModelId", "model_id", "modelID", "id", "ID"}),
		RAMRequiredBytes: getUintField(p, []string{"RAMRequiredBytes", "RamRequiredBytes", "ram_required_bytes", "ramRequiredBytes"}),
		TTLSecs:          int(getIntField(p, []string{"TTLSecs", "TtlSecs", "ttl_secs", "ttlSeconds", "TTLSeconds"})),
		Priority:         int(getIntField(p, []string{"Priority", "priority"})),
		Pinned:           getBoolField(p, []string{"Pinned", "pinned"}),
	}
	return row
}

func getStringField(v reflect.Value, names []string) string {
	f := findField(v, names)
	if !f.IsValid() {
		return ""
	}
	if f.Kind() == reflect.String {
		return f.String()
	}
	return ""
}

func getUintField(v reflect.Value, names []string) uint64 {
	f := findField(v, names)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		return f.Uint()
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		if f.Int() < 0 {
			return 0
		}
		return uint64(f.Int())
	}
	return 0
}

func getIntField(v reflect.Value, names []string) int64 {
	f := findField(v, names)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		return f.Int()
	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		return int64(f.Uint())
	}
	return 0
}

func getBoolField(v reflect.Value, names []string) bool {
	f := findField(v, names)
	if !f.IsValid() {
		return false
	}
	if f.Kind() == reflect.Bool {
		return f.Bool()
	}
	return false
}

func setStringField(v reflect.Value, names []string, val string) {
	f := findFieldWritable(v, names)
	if f.IsValid() && f.Kind() == reflect.String {
		f.SetString(val)
	}
}

func setUintField(v reflect.Value, names []string, val uint64) {
	f := findFieldWritable(v, names)
	if !f.IsValid() {
		return
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		f.SetUint(val)
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		f.SetInt(int64(val))
	}
}

func setIntField(v reflect.Value, names []string, val int) {
	f := findFieldWritable(v, names)
	if !f.IsValid() {
		return
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		f.SetInt(int64(val))
	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		if val < 0 {
			f.SetUint(0)
		} else {
			f.SetUint(uint64(val))
		}
	}
}

func setBoolField(v reflect.Value, names []string, val bool) {
	f := findFieldWritable(v, names)
	if f.IsValid() && f.Kind() == reflect.Bool {
		f.SetBool(val)
	}
}

func findField(v reflect.Value, names []string) reflect.Value {
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	for _, name := range names {
		f := v.FieldByName(name)
		if f.IsValid() {
			return f
		}
	}
	return reflect.Value{}
}

func findFieldWritable(v reflect.Value, names []string) reflect.Value {
	// Dereference pointers and ensure addressable.
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}

	for _, name := range names {
		f := v.FieldByName(name)
		if f.IsValid() && f.CanSet() {
			return f
		}
	}
	return reflect.Value{}
}

type errString string

func (e errString) Error() string { return string(e) }
