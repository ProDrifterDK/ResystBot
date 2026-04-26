package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
)

type promptLayer string

const (
	promptLayerSystem promptLayer = "system"
	promptLayerUser   promptLayer = "user"
)

type promptSlot string

const (
	slotIdentity    promptSlot = "identity"
	slotBootstrap   promptSlot = "bootstrap"
	slotSkillsIndex promptSlot = "skills-index"
	slotAutoSkills  promptSlot = "auto-skills"
	slotMemory      promptSlot = "memory"
	slotSession     promptSlot = "session"
	slotSummary     promptSlot = "summary"
	slotTeamForge   promptSlot = "teamforge"
)

var promptSlotOrder = map[promptSlot]int{
	slotIdentity:    0,
	slotBootstrap:   1,
	slotSkillsIndex: 2,
	slotAutoSkills:  3,
	slotMemory:      4,
	slotSession:     5,
	slotTeamForge:   6,
	slotSummary:     7,
}

type promptPart struct {
	ID      string
	Layer   promptLayer
	Slot    promptSlot
	Title   string
	Content string
}

type promptContributor interface {
	contributePrompt(ctx context.Context) ([]promptPart, error)
}

type promptRegistry struct {
	mu           sync.RWMutex
	contributors []promptContributor
}

func newPromptRegistry() *promptRegistry {
	return &promptRegistry{}
}

func (r *promptRegistry) register(c promptContributor) {
	if c == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.contributors = append(r.contributors, c)
}

func (r *promptRegistry) collect(ctx context.Context) ([]promptPart, error) {
	r.mu.RLock()
	contributors := make([]promptContributor, len(r.contributors))
	copy(contributors, r.contributors)
	r.mu.RUnlock()

	allParts := make([]promptPart, 0)
	for _, c := range contributors {
		parts, err := c.contributePrompt(ctx)
		if err != nil {
			continue
		}
		allParts = append(allParts, parts...)
	}

	sort.SliceStable(allParts, func(i, j int) bool {
		oi, okI := promptSlotOrder[allParts[i].Slot]
		oj, okJ := promptSlotOrder[allParts[j].Slot]
		if !okI {
			oi = 999
		}
		if !okJ {
			oj = 999
		}
		return oi < oj
	})

	return allParts, nil
}

func renderPromptParts(parts []promptPart, sep string) string {
	contents := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Content != "" {
			contents = append(contents, p.Content)
		}
	}
	if len(contents) == 0 {
		return ""
	}
	return strings.Join(contents, sep)
}
