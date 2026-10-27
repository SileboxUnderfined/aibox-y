package aibox

import (
	"regexp"
	"strings"

	"aiboxy/internal/chats"
)

type kind int

const (
	cmdNone kind = iota
	cmdNew
	cmdContinue
	cmdTemporary
	cmdForget
	cmdList
	cmdExit
	cmdModel       // explicit "модель X": answers even when X is unknown
	cmdModelSwitch // "переключись на X": only when X names a model
	cmdWhichModel
	cmdSetPrompt
	cmdResetPrompt
	cmdShowPrompt
)

type command struct {
	kind kind
	name string
}

var patterns = []struct {
	kind kind
	re   *regexp.Regexp
}{
	{cmdNew, regexp.MustCompile(`^(?:создай |создать |начни |начать |открой )?нов(?:ый|ой) чат(?: (.+))?$`)},
	{cmdContinue, regexp.MustCompile(`^(?:продолжи(?:ть|м)?|продолжаем|открой|открыть|вернись в|вернуться в|верни) чат (.+)$`)},
	{cmdTemporary, regexp.MustCompile(`^(?:давай )?поговори(?:ть|м)(?: со мной)?$`)},
	{cmdForget, regexp.MustCompile(`^(?:забудь|забыть|удали|удалить) чат (.+)$`)},
	{cmdList, regexp.MustCompile(`^(?:список чатов|мои чаты|какие (?:у меня )?(?:есть )?чаты(?: есть)?)$`)},
	{cmdWhichModel, regexp.MustCompile(`^(?:какая (?:сейчас )?модель(?: сейчас)?|список моделей|какие (?:есть )?модели(?: есть)?)$`)},
	{cmdModel, regexp.MustCompile(`^(?:выбери|выбрать|смени|сменить|поставь|переключи|переключись на|используй)? ?модель (?:на )?(.+)$`)},
	{cmdModelSwitch, regexp.MustCompile(`^(?:переключись на|переключи на|переключиться на|используй|давай) (.+)$`)},
	{cmdResetPrompt, regexp.MustCompile(`^(?:сбрось|сбросить|удали|убери|очисти) (?:системный промпт|промпт|роль)$`)},
	{cmdShowPrompt, regexp.MustCompile(`^(?:какой (?:сейчас )?(?:системный )?промпт|какая (?:у тебя )?роль)$`)},
	{cmdSetPrompt, regexp.MustCompile(`^(?:(?:установи |поставь |новый |смени )?системный промпт|(?:установи |новая |смени )роль|твоя роль) (?:на |это )?(.+)$`)},
}

var exitPhrases = regexp.MustCompile(`^(?:хватит|стоп|выход|выйти|выйди|пока|закрой чат|закрыть чат|выйди из чата|выйти из чата|заверши чат|завершить чат|конец чата)$`)

// cleanText normalizes recognized speech and drops the wake word.
func cleanText(text string) string {
	t := chats.Normalize(text)
	for _, wake := range []string{"алиса ", "алис "} {
		t = strings.TrimPrefix(t, wake)
	}
	return t
}

// parse recognizes a chat command; exit phrases count only inside a chat.
func parse(text string, inChat bool) command {
	t := cleanText(text)
	for _, p := range patterns {
		if m := p.re.FindStringSubmatch(t); m != nil {
			c := command{kind: p.kind}
			if len(m) > 1 {
				c.name = strings.TrimSpace(m[1])
			}
			return c
		}
	}
	if inChat && exitPhrases.MatchString(t) {
		return command{kind: cmdExit}
	}
	return command{kind: cmdNone}
}
