package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func skillByName(list []*Skill, name string) *Skill {
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func TestWriteAndDiscoverManagedSkill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(ManagedSkillsDirEnv, tmp)

	path, err := WriteManagedSkill("my-skill", "Use when doing X.\nSecond line", "Body instructions here.", true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(tmp, "my-skill", SkillFileName), path)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	// Description is collapsed to a single frontmatter line.
	require.Contains(t, string(content), "description: Use when doing X. Second line")
	require.Contains(t, string(content), "name: my-skill")
	require.Contains(t, string(content), "Body instructions here.")

	// Exclusive create: a second create fails; update succeeds.
	_, err = WriteManagedSkill("my-skill", "d", "b", true)
	require.Error(t, err)
	_, err = WriteManagedSkill("my-skill", "d", "new body", false)
	require.NoError(t, err)

	// Discovery marks the skill managed and includes it in the active set.
	_, active, _ := DiscoverFromConfig(DiscoveryConfig{ManagedSkillsDir: tmp})
	got := skillByName(active, "my-skill")
	require.NotNil(t, got)
	require.True(t, got.Managed)
}

func TestManagedSkillOverriddenByUserSkill(t *testing.T) {
	managed := t.TempDir()
	t.Setenv(ManagedSkillsDirEnv, managed)
	_, err := WriteManagedSkill("dup", "managed desc", "managed body", true)
	require.NoError(t, err)

	// A same-named user skill sits at higher precedence than managed.
	userDir := t.TempDir()
	skillDir := filepath.Join(userDir, "dup")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, SkillFileName),
		[]byte("---\nname: dup\ndescription: user desc\n---\nuser body\n"),
		0o644,
	))

	_, active, _ := DiscoverFromConfig(DiscoveryConfig{
		ManagedSkillsDir: managed,
		SkillsPaths:      []string{userDir},
	})
	got := skillByName(active, "dup")
	require.NotNil(t, got)
	require.False(t, got.Managed, "user skill must override the managed skill")
	require.Equal(t, "user desc", got.Description)
}

func TestDeleteManagedSkill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(ManagedSkillsDirEnv, tmp)
	_, err := WriteManagedSkill("gone", "d", "b", true)
	require.NoError(t, err)

	require.NoError(t, DeleteManagedSkill("gone"))
	require.Error(t, DeleteManagedSkill("gone"), "deleting a missing skill errors")

	_, statErr := os.Stat(filepath.Join(tmp, "gone"))
	require.True(t, os.IsNotExist(statErr))
}

func TestNormalizeManagedName(t *testing.T) {
	for _, bad := range []string{"", "Bad Name", "-lead", "trail-", "a/b", "a--b?"} {
		_, err := NormalizeManagedName(bad)
		require.Error(t, err, "expected %q to be rejected", bad)
	}
	got, err := NormalizeManagedName("  Good-Name1  ")
	require.NoError(t, err)
	require.Equal(t, "good-name1", got)
}

func TestWriteManagedSkillRejectsEmptyParts(t *testing.T) {
	t.Setenv(ManagedSkillsDirEnv, t.TempDir())
	_, err := WriteManagedSkill("x", "   ", "body", true)
	require.Error(t, err)
	_, err = WriteManagedSkill("x", "desc", "   ", true)
	require.Error(t, err)
}
