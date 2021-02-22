package types

type BuildFiles struct {
	Build *Build              `json:"build"`
	Files map[string]struct{} `json:"files"`
}

func (f BuildFiles) Has(filename string) bool {
	_, ok := f.Files[f.Build.GCSPrefix+filename]
	return ok
}
