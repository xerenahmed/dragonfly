package playerdb

import "github.com/df-mc/dragonfly/server/world"

const (
	survival = uint8(iota)
	creative
	adventure
	spectator
)

func gameModeToData(mode world.GameMode) uint8 {
	switch mode {
	case world.GameModes.Creative:
		return creative
	case world.GameModes.Adventure:
		return adventure
	case world.GameModes.Spectator:
		return spectator
	default:
		return survival
	}
}

func dataToGameMode(mode uint8) world.GameMode {
	switch mode {
	case creative:
		return world.GameModes.Creative
	case adventure:
		return world.GameModes.Adventure
	case spectator:
		return world.GameModes.Spectator
	default:
		return world.GameModes.Survival
	}
}
