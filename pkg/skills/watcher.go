package skills

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// SkillWatcher watches skill directories for changes and notifies via callback.
type SkillWatcher struct {
	loader   *SkillsLoader
	watcher  *fsnotify.Watcher
	debounce time.Duration
	onChange func(skillName string, event string)
	mu       sync.Mutex
	timers   map[string]*time.Timer // path → debounce timer
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewSkillWatcher creates a new watcher for the given skills loader.
// The onChange callback receives (skillName, eventType) where eventType is "created", "modified", or "deleted".
// Default debounce is 500ms.
func NewSkillWatcher(loader *SkillsLoader, onChange func(skillName, event string)) (*SkillWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	sw := &SkillWatcher{
		loader:   loader,
		watcher:  fw,
		debounce: 500 * time.Millisecond,
		onChange: onChange,
		timers:   make(map[string]*time.Timer),
		ctx:      ctx,
		cancel:   cancel,
	}

	return sw, nil
}

// Start begins watching all configured skill directories.
func (sw *SkillWatcher) Start() error {
	// Watch workspace, global, and builtin skill directories.
	dirs := []string{sw.loader.workspaceSkills, sw.loader.globalSkills, sw.loader.builtinSkills}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := sw.addWatch(dir); err != nil {
			logger.WarnCF("skills", "Failed to watch skill directory",
				map[string]any{"dir": dir, "error": err.Error()})
			continue
		}
	}

	go sw.eventLoop()
	return nil
}

// Stop gracefully shuts down the watcher.
func (sw *SkillWatcher) Stop() {
	sw.cancel()
	_ = sw.watcher.Close()

	// Clear pending timers.
	sw.mu.Lock()
	for _, t := range sw.timers {
		t.Stop()
	}
	sw.timers = make(map[string]*time.Timer)
	sw.mu.Unlock()
}

// SetDebounce changes the debounce interval. Must be called before Start().
func (sw *SkillWatcher) SetDebounce(d time.Duration) {
	sw.debounce = d
}

func (sw *SkillWatcher) eventLoop() {
	for {
		select {
		case <-sw.ctx.Done():
			return
		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			sw.handleEvent(event)
		case err, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
			logger.WarnCF("skills", "Watcher error",
				map[string]any{"error": err.Error()})
		}
	}
}

func (sw *SkillWatcher) handleEvent(event fsnotify.Event) {
	if event.Op&fsnotify.Create == fsnotify.Create {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if err := sw.addWatch(event.Name); err != nil {
				logger.WarnCF("skills", "Failed to watch new skill directory",
					map[string]any{"dir": event.Name, "error": err.Error()})
				return
			}

			skillFile := filepath.Join(event.Name, "SKILL.md")
			if _, err := os.Stat(skillFile); err == nil {
				sw.queueCallback(skillFile, filepath.Base(event.Name), "created")
			}
			return
		}
	}

	// We only care about SKILL.md files.
	if filepath.Base(event.Name) != "SKILL.md" {
		return
	}

	// Extract skill name from parent directory.
	skillName := filepath.Base(filepath.Dir(event.Name))

	// Determine event type.
	var eventType string
	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		eventType = "created"
	case event.Op&fsnotify.Write == fsnotify.Write:
		eventType = "modified"
	case event.Op&fsnotify.Remove == fsnotify.Remove:
		eventType = "deleted"
	case event.Op&fsnotify.Rename == fsnotify.Rename:
		eventType = "deleted"
	default:
		return
	}

	sw.queueCallback(event.Name, skillName, eventType)
}

func (sw *SkillWatcher) queueCallback(path, skillName, eventType string) {
	// Debounce: cancel existing timer for this path, set new one.
	sw.mu.Lock()
	if t, exists := sw.timers[path]; exists {
		t.Stop()
	}
	sw.timers[path] = time.AfterFunc(sw.debounce, func() {
		sw.mu.Lock()
		delete(sw.timers, path)
		sw.mu.Unlock()

		if sw.onChange != nil {
			sw.onChange(skillName, eventType)
		}
	})
	sw.mu.Unlock()
}

func (sw *SkillWatcher) addWatch(dir string) error {
	if err := sw.watcher.Add(dir); err != nil {
		return err
	}
	logger.DebugCF("skills", "Watching skill directory", map[string]any{"dir": dir})

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		// Follow symlinks: entry.IsDir() returns false for symlinks
		if fi, err := os.Stat(filepath.Join(dir, entry.Name())); err != nil || !fi.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		if err := sw.watcher.Add(skillDir); err != nil {
			logger.WarnCF("skills", "Failed to watch skill subdirectory",
				map[string]any{"dir": skillDir, "error": err.Error()})
			continue
		}
		logger.DebugCF("skills", "Watching skill subdirectory", map[string]any{"dir": skillDir})
	}

	return nil
}
