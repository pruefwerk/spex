package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
)

var requestID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var commitID = regexp.MustCompile(`^[a-f0-9]{40}$`)
var repositoryID = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func (h *Host) allowedGroupTags() map[string]bool {
	tags := map[string]bool{}
	for _, group := range h.config.Groups {
		tag := "group-" + group
		if group == "baseline" {
			tag = h.config.BaselineTag
		}
		tags[tag] = true
	}
	return tags
}

type policy struct {
	host                            *Host
	admitted                        admissionContext
	repository, sha, release, token string
	client                          *http.Client
}

func (p *policy) Admit(_ context.Context, e receiver.Envelope) error {
	x := p.admitted.Expected
	if e.RequestID != x.RequestID || e.Source.Repository != x.Repository || e.Source.Commit != x.Commit || e.Source.RunID != x.SourceRunID || e.Source.Root != "." {
		return errors.New("unadmitted source")
	}
	return nil
}

func (p *policy) Claim(ctx context.Context, id string) error {
	if !requestID.MatchString(id) || !repositoryID.MatchString(p.repository) || !commitID.MatchString(p.sha) || p.token == "" {
		return errors.New("request store unavailable")
	}
	// GitHub ref creation is atomic and durable across runners. Never update,
	// delete or expire these claims; a replay must use a new admitted request.
	data, _ := json.Marshal(map[string]string{"ref": "refs/tags/spex-request-" + id, "sha": p.sha})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/repos/"+p.repository+"/git/refs", bytes.NewReader(data))
	if err != nil {
		return errors.New("request store unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := p.client.Do(request)
	if err != nil {
		return errors.New("request claim not confirmed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return errors.New("request already claimed or claim unavailable")
	}
	return nil
}

func (p *policy) Allow(_ context.Context, s scenario.Scenario) error {
	if s.Runtime != p.host.config.Runtime || s.RuntimeRelease != p.release || len(s.Tests) != 0 || len(s.Dependencies) != 0 || !s.RuntimeOverlay.Empty() {
		return errors.New("only trusted runtime-discovered tests are admitted")
	}
	if s.Metadata.Timeout != nil {
		duration, err := time.ParseDuration(*s.Metadata.Timeout)
		if err != nil || duration <= 0 || duration > hostconfig.Deadline(p.host.config.MaximumTimeout) {
			return errors.New("invalid timeout")
		}
	}
	var overlay struct {
		Selection struct {
			IncludeTags    *[]string `toml:"include_tags"`
			IncludeAnyTags *[]string `toml:"include_any_tags"`
		} `toml:"selection"`
	}
	if err := s.RuntimeConfig.DecodeStrict(&overlay); err != nil {
		return errors.New("only group selection is admitted")
	}
	for _, tags := range []*[]string{overlay.Selection.IncludeTags, overlay.Selection.IncludeAnyTags} {
		if tags != nil {
			for _, tag := range *tags {
				if !p.host.allowedGroupTags()[tag] {
					return errors.New("unadmitted group")
				}
			}
		}
	}
	return nil
}

func (h *Host) makeHost(admitted admissionContext, runtime receiver.Implementation) (receiver.Host, error) {
	id, err := strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
	if err != nil || id <= 0 || os.Getenv("GITHUB_RUN_ATTEMPT") != "1" || !commitID.MatchString(os.Getenv("GITHUB_SHA")) || !repositoryID.MatchString(os.Getenv("GITHUB_REPOSITORY")) {
		return receiver.Host{}, fmt.Errorf("receiver execution identity invalid")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return receiver.Host{Policy: &policy{host: h, admitted: admitted, repository: os.Getenv("GITHUB_REPOSITORY"), sha: os.Getenv("GITHUB_SHA"), release: runtime.Release(), token: os.Getenv("RECEIVER_CLAIM_TOKEN"), client: client},
		Runtimes: []receiver.Choice{{Runtime: runtime, Default: true}}, DefaultRuntime: runtime.ID(),
		Execution: receiver.Execution{WorkflowSHA: os.Getenv("GITHUB_SHA"), RunID: id, RunAttempt: 1, SpexVersion: strings.TrimPrefix(h.version, "v")}}, nil
}
