package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutolearnDefaultsOn(t *testing.T) {
	t.Parallel()

	// nil Options: both capabilities default on.
	var nilOpts *Options
	require.True(t, nilOpts.LearnEnabled())
	require.True(t, nilOpts.ManageSkillEnabled())

	// Options present but no autolearn block: on.
	empty := &Options{}
	require.True(t, empty.LearnEnabled())
	require.True(t, empty.ManageSkillEnabled())

	off := false
	on := true

	// Master switch off disables both.
	master := &Options{Autolearn: &AutolearnOptions{Enabled: &off}}
	require.False(t, master.LearnEnabled())
	require.False(t, master.ManageSkillEnabled())

	// Per-capability off, master default on.
	skillOff := &Options{Autolearn: &AutolearnOptions{ManageSkill: &off}}
	require.True(t, skillOff.LearnEnabled())
	require.False(t, skillOff.ManageSkillEnabled())

	learnOff := &Options{Autolearn: &AutolearnOptions{Learn: &off}}
	require.False(t, learnOff.LearnEnabled())
	require.True(t, learnOff.ManageSkillEnabled())

	// Explicit on is honored.
	explicit := &Options{Autolearn: &AutolearnOptions{Enabled: &on, Learn: &on, ManageSkill: &on}}
	require.True(t, explicit.LearnEnabled())
	require.True(t, explicit.ManageSkillEnabled())
}
