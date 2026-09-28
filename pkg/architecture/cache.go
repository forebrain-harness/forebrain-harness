package architecture

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CacheUse is one provider's cache accounting for a replay case.
type CacheUse struct {
	Read     int64 `json:"cache_read_input_tokens"`
	Creation int64 `json:"cache_creation_input_tokens"`
	Input    int64 `json:"input_tokens"`
}

// Rate returns the cache-read ratio for this replay case.
func (u CacheUse) Rate() float64 {
	if total := u.Total(); total > 0 {
		return float64(u.Read) / float64(total)
	}
	return 0
}

// Total returns all input tokens reported by a replay case.
func (u CacheUse) Total() int64 {
	return u.Read + u.Creation + u.Input
}

// CacheRun groups replay cases for one provider and model.
type CacheRun struct {
	Model string              `json:"model"`
	Cases map[string]CacheUse `json:"cases"`
}

// CacheReport is the persisted cache-hit baseline or a replay result.
type CacheReport struct {
	Providers map[string]CacheRun `json:"providers"`
}

// LoadCacheReport decodes a cache baseline or replay result.
func LoadCacheReport(r io.Reader) (CacheReport, error) {
	var report CacheReport
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return CacheReport{}, err
	}
	return report, validateCacheReport(report)
}

// CheckCache rejects missing data and hit-rate regressions.
func CheckCache(base, current CacheReport) error {
	if err := validateCacheReport(base); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := validateCacheReport(current); err != nil {
		return fmt.Errorf("current: %w", err)
	}
	var errs []string
	for _, provider := range sortedKeys(base.Providers) {
		before := base.Providers[provider]
		after, ok := current.Providers[provider]
		if !ok {
			errs = append(errs, provider+": missing provider")
			continue
		}
		for _, name := range sortedKeys(before.Cases) {
			old := before.Cases[name]
			new, ok := after.Cases[name]
			if !ok {
				errs = append(errs, provider+"/"+name+": missing case")
				continue
			}
			if new.Read*old.Total() < old.Read*new.Total() {
				errs = append(errs, provider+"/"+name+": cache hit rate regressed")
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cache regression: %s", strings.Join(errs, "; "))
	}
	return nil
}

func validateCacheReport(report CacheReport) error {
	if len(report.Providers) == 0 {
		return fmt.Errorf("no providers")
	}
	for provider, run := range report.Providers {
		if strings.TrimSpace(provider) == "" || strings.TrimSpace(run.Model) == "" || len(run.Cases) == 0 {
			return fmt.Errorf("invalid provider %q", provider)
		}
		for name, use := range run.Cases {
			if strings.TrimSpace(name) == "" || use.Read < 0 || use.Creation < 0 || use.Input < 0 || use.Total() == 0 {
				return fmt.Errorf("invalid case %q for %s", name, provider)
			}
		}
	}
	return nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
