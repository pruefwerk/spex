// Execution identity grants neither deployment nor deletion authority.
package definition

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
)

func scopeSchema() string { return Protocol("execution-scope") }

type Scope struct {
	owner      *Resolver
	Schema     string `json:"schema"`
	Repository string `json:"receiver_repository"`
	RunID      int64  `json:"run_id"`
	Attempt    int64  `json:"run_attempt"`
	Request    string `json:"request_id"`
	Nonce      string `json:"nonce"`
	Target     string `json:"target"`
}

var repository = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)
var allocation = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s Scope) Validate() error {
	var expected string
	if s.owner != nil {
		expected = s.owner.Protocol("execution-scope")
	} else {
		expected = scopeSchema()
	}
	if s.Schema != expected || !repository.MatchString(s.Repository) || s.RunID <= 0 || s.Attempt <= 0 || !allocation.MatchString(s.Request) || !allocation.MatchString(s.Nonce) || (s.Target != "kind" && s.Target != "aws") {
		return errors.New("invalid execution scope")
	}
	return nil
}

// Name preserves the existing identity algorithm and therefore existing ledgers.
// Scope identity grants neither permission to deploy nor permission to delete.
func (s Scope) Name() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{"receiver_repository": s.Repository, "run_id": s.RunID, "run_attempt": s.Attempt, "request_id": s.Request, "nonce": s.Nonce, "target": s.Target})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	var prefix string
	if s.owner != nil {
		prefix = s.owner.snapshot.ScopePrefix
	} else {
		prefix = Current().ScopePrefix
	}
	return prefix + "-" + hex.EncodeToString(hash[:])[:32], nil
}

func (s Scope) Labels() (map[string]string, error) {
	name, err := s.Name()
	if err != nil {
		return nil, err
	}
	return map[string]string{"spex.pruefwerk.dev/execution": name, "spex.pruefwerk.dev/request": s.Request, "spex.pruefwerk.dev/run": strconv.FormatInt(s.RunID, 10), "spex.pruefwerk.dev/attempt": strconv.FormatInt(s.Attempt, 10)}, nil
}

func (s Scope) Owns(labels map[string]string) bool {
	want, err := s.Labels()
	if err != nil {
		return false
	}
	for key, value := range want {
		if labels[key] != value {
			return false
		}
	}
	return true
}

type ResourceNames struct {
	Namespace         string  `json:"namespace"`
	KindCluster       *string `json:"kind_cluster"`
	ImageTag          string  `json:"image_tag"`
	HelmReleasePrefix string  `json:"helm_release_prefix"`
}

func (s Scope) Resources() (ResourceNames, error) {
	name, err := s.Name()
	if err != nil {
		return ResourceNames{}, err
	}
	result := ResourceNames{Namespace: name, ImageTag: name, HelmReleasePrefix: name}
	if s.Target == "kind" {
		result.KindCluster = &name
	}
	return result, nil
}

func (r *Resolver) Allocate(repository string, run, attempt int64, request, target string) (Scope, error) {
	s := Scope{owner: r, Schema: r.Protocol("execution-scope"), Repository: repository, RunID: run, Attempt: attempt, Request: request, Target: target, Nonce: "00000000000000000000000000000000"}
	if err := s.Validate(); err != nil {
		return Scope{}, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return Scope{}, errors.New("execution allocation failed")
	}
	s.Nonce = hex.EncodeToString(nonce)
	return s, nil
}

func SaveScope(path string, scope Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func (r *Resolver) LoadScope(path string) (Scope, error) {
	var s Scope
	if err := readDocument(path, &s, true); err != nil {
		return s, err
	}
	s.owner = r
	return s, s.Validate()
}
