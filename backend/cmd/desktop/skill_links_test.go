package main

import "testing"

func TestSkillInstanceIDsAreScopedToWorkspace(t *testing.T) {
	if skillInstanceID("one") == skillInstanceID("two") {
		t.Fatal("isolated data directories share lock")
	}
}
