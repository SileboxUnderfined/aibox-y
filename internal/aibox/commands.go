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
	cmdQuick // "спроси [модель|нейросеть] X": a new saved chat that starts with question X
	cmdRemember
	cmdForgetFact
	cmdForgetAll
	cmdShowMemory
	cmdMemoryOn
	cmdMemoryOff
	cmdAutoMemoryOn
	cmdAutoMemoryOff
)

// isMemory marks commands that work only while the shared memory is on.
func (k kind) isMemory() bool {
	return k == cmdRemember || k == cmdForgetFact || k == cmdForgetAll || k == cmdShowMemory
}

// isMemorySwitch marks commands that turn the shared memory on and off.
func (k kind) isMemorySwitch() bool {
	return k == cmdMemoryOn || k == cmdMemoryOff || k == cmdAutoMemoryOn || k == cmdAutoMemoryOff
}

type command struct {
	kind kind
	name string
}

var patterns = []struct {
	kind kind
	re   *regexp.Regexp
}{
	{cmdQuick, quickPattern},
	{cmdNew, regexp.MustCompile(`^(?:создай |создать |начни |начать |открой )?нов(?:ый|ой) чат(?: (.+))?$`)},
	{cmdContinue, regexp.MustCompile(`^(?:продолжи(?:ть|м)?|продолжаем|открой|открыть|вернись в|вернуться в|верни) чат (.+)$`)},
	{cmdTemporary, regexp.MustCompile(`^(?:давай )?поговори(?:ть|м)(?: со мной)?$`)},
	{cmdForget, regexp.MustCompile(`^(?:забудь|забыть|удали|удалить) чат (.+)$`)},
	{cmdForgetAll, regexp.MustCompile(`^(?:(?:забудь|забудьте|сотри|удали) (?:все|всю) (?:обо мне|про меня|что (?:ты )?(?:знаешь|помнишь|запомнила) обо мне)|(?:очисти|сотри|удали) (?:всю )?память)$`)},
	{cmdShowMemory, regexp.MustCompile(`^(?:что ты (?:обо мне |про меня )?(?:знаешь|помнишь|запомнила)(?: обо мне| про меня)?|что (?:есть )?в памяти|покажи память)$`)},
	{cmdAutoMemoryOff, regexp.MustCompile(`^(?:выключи|отключи) (?:авто ?запоминание|запоминание)$`)},
	{cmdAutoMemoryOn, regexp.MustCompile(`^(?:включи) (?:авто ?запоминание|запоминание)$`)},
	{cmdMemoryOff, regexp.MustCompile(`^(?:выключи|отключи) (?:общую )?память$`)},
	{cmdMemoryOn, regexp.MustCompile(`^включи (?:общую )?память$`)},
	{cmdRemember, rememberPattern},
	{cmdForgetFact, forgetFactPattern},
	{cmdList, regexp.MustCompile(`^(?:список чатов|мои чаты|какие (?:у меня )?(?:есть )?чаты(?: есть)?)$`)},
	{cmdWhichModel, regexp.MustCompile(`^(?:какая (?:сейчас )?модель(?: сейчас)?|список моделей|какие (?:есть )?модели(?: есть)?)$`)},
	{cmdModel, regexp.MustCompile(`^(?:выбери|выбрать|смени|сменить|поставь|переключи|переключись на|используй)? ?модель (?:на )?(.+)$`)},
	{cmdModelSwitch, regexp.MustCompile(`^(?:переключись на|переключи на|переключиться на|используй|давай) (.+)$`)},
	{cmdResetPrompt, regexp.MustCompile(`^(?:сбрось|сбросить|удали|убери|очисти) (?:системный промпт|промпт|роль)$`)},
	{cmdShowPrompt, regexp.MustCompile(`^(?:какой (?:сейчас )?(?:системный )?промпт|какая (?:у тебя )?роль)$`)},
	{cmdSetPrompt, regexp.MustCompile(`^(?:(?:установи |поставь |новый |смени )?системный промпт|(?:установи |новая |смени )роль|твоя роль) (?:на |это )?(.+)$`)},
}

// quickPattern starts a quick question, in normalized text. The neuro group
// marks "спроси нейросеть", which never names a model.
var quickPattern = regexp.MustCompile(`^(?:(?:спроси(?:те)?|задай(?:те)? вопрос)(?: у)?(?P<neuro> (?:нейросет[ьи]|нейронк[уеи]|ии))?|вопрос(?P<neuro2> (?:нейросет[ьи]|нейронк[уеи]|ии)))(?: (.+))?$`)

// rememberPattern and forgetFactPattern keep the fact in their last group.
var (
	rememberPattern   = regexp.MustCompile(`^(?:запомни|запомните)(?: пожалуйста)?(?: что| обо мне| про меня)? (.+)$`)
	forgetFactPattern = regexp.MustCompile(`^(?:забудь|забудьте)(?: пожалуйста)?(?: что| про| о| об)? (.+)$`)
)

// stopPhrases cut a spoken answer short; the chat stays open.
var stopPhrases = regexp.MustCompile(`^(?:стоп|хватит|замолчи|помолчи|тихо|тише|подожди|погоди|достаточно|остановись|перестань|не надо|ладно|понятно|ясно)$`)

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
