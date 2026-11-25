package awake

import (
	"os/exec"
	"sync"
)

type Controller struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func New() *Controller {
	return &Controller{}
}

func (c *Controller) Set(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if enabled {
		if c.cmd != nil && c.cmd.Process != nil {
			return
		}
		cmd := exec.Command("/usr/bin/caffeinate", "-dimsu")
		if err := cmd.Start(); err == nil {
			c.cmd = cmd
			go func(p *exec.Cmd) {
				_ = p.Wait()
				c.mu.Lock()
				if c.cmd == p {
					c.cmd = nil
				}
				c.mu.Unlock()
			}(cmd)
		}
		return
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		c.cmd = nil
	}
}

func (c *Controller) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil && c.cmd.Process != nil
}
