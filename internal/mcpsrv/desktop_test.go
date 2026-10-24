package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// TestTheDesktopToolsRefuseACPUOnlyNode checks that the desktop tools
// exist and that they refuse honestly: the node runs command line
// workloads only, so it answers with the capability_unavailable code and an
// explanation instead of a session it cannot serve.
func TestTheDesktopToolsRefuseACPUOnlyNode(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	instance, err := env.store.CreateInstance(context.Background(), store.Instance{
		ID:         ids.New(ids.PrefixInstance),
		JobID:      ids.New(ids.PrefixJob),
		UserID:     env.user.ID,
		TemplateID: "alpine-shell",
		Runtime:    "local",
		RuntimeID:  "local-1",
		Name:       "desktop",
		State:      store.InstanceReady,
		ExpiresAt:  time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create the instance: %v", err)
	}

	for _, name := range []string{"desktop.open", "desktop.close"} {
		result := callTool(t, session, name, map[string]any{"instance_id": instance.ID})
		if !result.IsError {
			t.Fatalf("the call of %s succeeded", name)
		}
		if code := toolErrorCode(t, result); code != string(CodeCapabilityUnavailable) {
			t.Errorf("the failure of %s carries the code %q, want %q", name, code, CodeCapabilityUnavailable)
		}
		message := toolErrorText(t, result)
		if !strings.Contains(message, "desktop") {
			t.Errorf("the failure of %s does not explain the missing capability: %s", name, message)
		}
		if !strings.Contains(message, "instance.exec") && !strings.Contains(message, "instance.destroy") {
			t.Errorf("the failure of %s does not offer an alternative: %s", name, message)
		}
	}
}

// TestTheDesktopToolsRefuseAnUnknownInstance checks that the tools never
// leak the existence of an instance of another account.
func TestTheDesktopToolsRefuseAnUnknownInstance(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	result := callTool(t, session, "desktop.open", map[string]any{"instance_id": "ins_01JUNKNOWN"})
	if !result.IsError {
		t.Fatal("the call for an unknown instance succeeded")
	}
	if code := toolErrorCode(t, result); code != string(CodeNotFound) {
		t.Errorf("the failure carries the code %q, want %q", code, CodeNotFound)
	}
}
