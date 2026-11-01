package bridge

import (
	"context"

	"aiboxy/internal/characters"
)

// CharacterSource supplies the global active character to the dialogue answer
// path. It is optional: nil keeps the previous system prompt unchanged.
type CharacterSource interface {
	Active() characters.Character
}

type characterSnapshotKey struct{}

// snapshotCharacter stores the active character once per request. Every later
// stage of that request reads this value, so switching the persona while an
// answer is being produced cannot rewrite the prompt in flight.
func snapshotCharacter(ctx context.Context, source CharacterSource) context.Context {
	active := characters.Default()
	if source != nil {
		active = source.Active()
	}
	return context.WithValue(ctx, characterSnapshotKey{}, active)
}

func characterSnapshot(ctx context.Context) (characters.Character, bool) {
	active, ok := ctx.Value(characterSnapshotKey{}).(characters.Character)
	return active, ok
}
