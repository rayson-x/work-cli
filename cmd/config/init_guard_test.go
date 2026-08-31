// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
)

func TestConfigInitHelpUsesOneHostIndependentContract(t *testing.T) {
	cmd := NewCmdConfigInit(nil, nil)
	if cmd.Flags().Lookup("force-init") != nil {
		t.Fatal("config init must not expose the former Agent-workspace escape hatch")
	}
	for _, forbidden := range []string{"HERMES_HOME", "OPENCLAW_HOME", "config bind", "Agent workspace"} {
		if strings.Contains(cmd.Long, forbidden) {
			t.Fatalf("config init help contains obsolete host-specific guidance %q:\n%s", forbidden, cmd.Long)
		}
	}
	if !strings.Contains(cmd.Long, "same work-cli configuration file") {
		t.Fatalf("config init help does not explain the single-config contract:\n%s", cmd.Long)
	}
}
