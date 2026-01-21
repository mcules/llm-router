package policy

type ModelPolicy struct {
	ModelID          string
	RAMRequiredBytes uint64
	TTLSecs          int64
	Pinned           bool
	Priority         int // higher = keep longer
}
