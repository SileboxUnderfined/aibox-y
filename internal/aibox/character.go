package aibox

import (
	"errors"
	"log/slog"
	"strings"

	"aiboxy/internal/characters"
)

// ErrCharactersUnavailable means the resolver was built without a character
// store. The panel still loads; only the character API reports the gap.
var ErrCharactersUnavailable = errors.New("characters are not configured")

// CharacterInfo is one persona as the panel sees it. The built-in default is
// returned with the same shape, so every selection renders uniformly.
type CharacterInfo struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Prompt string   `json:"prompt"`
	Rules  []string `json:"rules"`
	Active bool     `json:"active"`
}

// CharacterInput is the only data the panel may change: an id is never taken
// from the client on create and the built-in default is reserved.
type CharacterInput struct {
	Name   string   `json:"name"`
	Prompt string   `json:"prompt"`
	Rules  []string `json:"rules"`
}

// CharacterLimits mirrors the store's server-side field bounds so the panel
// can set maxlength hints without inventing looser limits of its own.
type CharacterLimits struct {
	Name   int `json:"name"`
	Prompt int `json:"prompt"`
	Rules  int `json:"rules"`
	Rule   int `json:"rule"`
}

// CharactersInfo is the complete global character state: the stored list,
// the active id, the built-in fallback and the field limits.
type CharactersInfo struct {
	Characters []CharacterInfo `json:"characters"`
	Active     string          `json:"active"`
	Default    CharacterInfo   `json:"default"`
	Limits     CharacterLimits `json:"limits"`
}

// Characters returns the global list and active selection. It never reads or
// changes provider settings, prompts or secrets.
func (r *Resolver) Characters() (CharactersInfo, error) {
	if r.opt.Characters == nil {
		return CharactersInfo{}, ErrCharactersUnavailable
	}
	list, active := r.opt.Characters.Snapshot()
	info := CharactersInfo{
		Characters: []CharacterInfo{},
		Active:     active,
		Default:    characterInfo(characters.Default(), active),
		Limits: CharacterLimits{
			Name:   characters.MaxNameRunes,
			Prompt: characters.MaxPromptRunes,
			Rules:  characters.MaxRules,
			Rule:   characters.MaxRuleRunes,
		},
	}
	for _, c := range list {
		info.Characters = append(info.Characters, characterInfo(c, active))
	}
	return info, nil
}

// CreateCharacter adds a persona. The store generates the id, validates the
// fields and persists the global list.
func (r *Resolver) CreateCharacter(in CharacterInput) (CharacterInfo, error) {
	if r.opt.Characters == nil {
		return CharacterInfo{}, ErrCharactersUnavailable
	}
	c, err := r.opt.Characters.Create(characters.Character{Name: in.Name, Prompt: in.Prompt, Rules: in.Rules})
	if err != nil {
		return CharacterInfo{}, err
	}
	return characterInfo(c, r.opt.Characters.ActiveID()), nil
}

// UpdateCharacter changes name, prompt and rules while keeping the id stable.
func (r *Resolver) UpdateCharacter(id string, in CharacterInput) (CharacterInfo, error) {
	if r.opt.Characters == nil {
		return CharacterInfo{}, ErrCharactersUnavailable
	}
	c, err := r.opt.Characters.Update(characters.Character{ID: id, Name: in.Name, Prompt: in.Prompt, Rules: in.Rules})
	if err != nil {
		return CharacterInfo{}, err
	}
	return characterInfo(c, r.opt.Characters.ActiveID()), nil
}

// DeleteCharacter removes a persona; deleting the active one falls back to the
// built-in default.
func (r *Resolver) DeleteCharacter(id string) error {
	if r.opt.Characters == nil {
		return ErrCharactersUnavailable
	}
	return r.opt.Characters.Delete(id)
}

// SetActiveCharacter applies the selection to every chat. DefaultID resets it.
func (r *Resolver) SetActiveCharacter(id string) error {
	if r.opt.Characters == nil {
		return ErrCharactersUnavailable
	}
	return r.opt.Characters.SetActive(id)
}

func characterInfo(c characters.Character, active string) CharacterInfo {
	rules := append([]string{}, c.Rules...)
	return CharacterInfo{ID: c.ID, Name: c.Name, Prompt: c.Prompt, Rules: rules, Active: c.ID == active}
}

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
