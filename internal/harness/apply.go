package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func applyPlan(p Plan) error {
	stamp := time.Now().Unix()
	for _, c := range p.Changes {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(c.Path), err)
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(c.Path); err == nil {
			mode = st.Mode().Perm()
			if err := os.WriteFile(fmt.Sprintf("%s.forge-bak-%d", c.Path, stamp), []byte(c.Before), mode); err != nil {
				return fmt.Errorf("backup %s: %w", c.Path, err)
			}
		}
		if c.After == "" {
			if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", c.Path, err)
			}
			continue
		}
		tmp := c.Path + ".forge-tmp"
		if err := os.WriteFile(tmp, []byte(c.After), mode); err != nil {
			return fmt.Errorf("write %s: %w", c.Path, err)
		}
		if err := os.Rename(tmp, c.Path); err != nil {
			return fmt.Errorf("replace %s: %w", c.Path, err)
		}
	}
	return nil
}
