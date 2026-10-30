package aibox

import (
	"log/slog"
	"strings"

	"aiboxy/internal/characters"
)

// characterAtStart snapshots the globally active character. A request keeps
// this value even if the active character is switched while it is running.
func (r *Resolver) characterAtStart() characters.Character {
	if r.opt.Characters == nil {
		return characters.Default()
	}
	return r.opt.Characters.Active()
}

// handleCharacter implements «персонаж [имя]». With no name it reports the
// current choice; «по умолчанию» clears it; an unknown name changes nothing.
// The command is answered locally and never reaches the LLM or chat history.
func (r *Resolver) handleCharacter(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		active := r.opt.Characters.Active()
		if active.IsDefault() {
			return "Сейчас персонаж «" + characters.DefaultName + "»: отдельный персонаж не выбран, отвечаю как обычно. " + r.characterListText()
		}
		return "Сейчас персонаж «" + active.Name + "». " + r.characterListText()
	}
	if characters.NormalizeName(name) == characters.NormalizeName(characters.DefaultName) {
		if err := r.opt.Characters.SetActive(characters.DefaultID); err != nil {
			slog.Warn("character_select_failed")
			return "Не получилось вернуть персонажа по умолчанию."
		}
		return "Вернула персонажа «" + characters.DefaultName + "». Отвечаю как обычно."
	}
	for _, c := range r.opt.Characters.List() {
		if characters.NormalizeName(c.Name) == characters.NormalizeName(name) {
			if err := r.opt.Characters.SetActive(c.ID); err != nil {
				slog.Warn("character_select_failed")
				return "Не получилось выбрать персонажа «" + c.Name + "»."
			}
			return "Теперь персонаж «" + c.Name + "». Модель не менялась."
		}
	}
	return "Не знаю персонажа «" + name + "». " + r.characterListText()
}

func (r *Resolver) characterListText() string {
	list := r.opt.Characters.List()
	if len(list) == 0 {
		return "Сохранённых персонажей пока нет."
	}
	names := make([]string, 0, len(list))
	for i, c := range list {
		if i == 8 {
			break
		}
		names = append(names, c.Name)
	}
	return "Доступные персонажи: " + strings.Join(names, ", ") + "."
}
