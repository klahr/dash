package screenshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// The pictures of task <id> are files under Root/<id>/, named by shotName.
// A new capture of the task removes its earlier run's; deleting the task
// removes the folder (Observe).

var todoIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrNoPicture answers a name that isn't one of a task's pictures.
var ErrNoPicture = errors.New("no such screenshot")

// dir is task id's folder, made if missing.
func (c *Capturer) dir(id string) (string, error) {
	if !todoIDRE.MatchString(id) || c.Root == "" {
		return "", errors.New("nowhere to keep the pictures")
	}
	d := filepath.Join(c.Root, id)
	return d, os.MkdirAll(d, 0o755)
}

// clearOld removes the pictures of the task's runs before run.
func (c *Capturer) clearOld(dir string, run int) error {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	keep := fmt.Sprintf("r%d-", run) // shotName's start
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), keep) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// Path is where task id's picture name is: only a name a capture makes.
func (c *Capturer) Path(id, name string) (string, error) {
	if c == nil || c.Root == "" || !todoIDRE.MatchString(id) || !ValidName(name) {
		return "", ErrNoPicture
	}
	return filepath.Join(c.Root, id, name), nil
}

// Remove deletes task id's pictures.
func (c *Capturer) Remove(id string) {
	if c == nil || c.Root == "" || !todoIDRE.MatchString(id) {
		return
	}
	_ = os.RemoveAll(filepath.Join(c.Root, id))
}

// Observe sees every store event: a deleted task's pictures go with it. It
// never blocks (the store calls it holding its lock).
func (c *Capturer) Observe(ev model.Event) {
	if ev.Type == model.EvTodoDeleted && ev.TodoID != "" {
		go c.Remove(ev.TodoID)
	}
}
