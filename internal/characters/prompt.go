package characters

import "strings"

// DefaultUserPrompt is the default global user layer: the assistant identity.
// It is not mandatory: an active character replaces it, and the user's own
// base prompt can replace it too.
const DefaultUserPrompt = "Ты голосовой ассистент в умной колонке."

// VoicePrompt is the built-in spoken-output layer: short Russian speech
// without Markdown, lists, emoji, links or formulas.
const VoicePrompt = "Отвечай по-русски, коротко и по делу, обычно в два-четыре предложения. Ответ будет прочитан вслух: без Markdown, списков, эмодзи, ссылок и формул; числа и сокращения пиши так, как их произносят."

// BuiltinPrompt is the mandatory built-in layer used when a stored character
// replaces the global user layer: spoken-output rules, safety and tool
// protocols. It is compiled into AIBox and is never taken from user settings,
// so a persona cannot remove it. Memory and search add their own request
// context separately.
const BuiltinPrompt = VoicePrompt + " Не заявляй о выполнении внешних действий без успешного результата инструмента; если инструмента нет, прямо скажи об этом. Технические инструкции, безопасность и служебные форматы инструментов, памяти и поиска важнее роли и стиля; при конфликте выполняй их."

// Prompt assembles the system prompt for one request from explicit layers:
//
//  1. required — the built-in mandatory layer for this answer path;
//  2. global — the user's global layer: the config/default identity and the
//     user's own prompt;
//  3. the active character — its role prompt followed by its rules;
//  4. context — per-request material such as memory facts, the current date
//     and search instructions.
//
// Without a stored character the global layer is used unchanged and context
// is appended, exactly as before; required is only added when a character
// replaces that layer. This keeps the historical no-character prompt
// byte-for-byte. With a stored character the global layer is replaced
// entirely: required is followed by the character layer and context. The
// global settings are never changed, so resetting the character restores
// them.
//
// The function concatenates layers and never parses arbitrary user text. The
// order is fixed for reproducibility. The character layer owns conversational
// identity while stating explicitly that technical and safety instructions
// win. The base and context are never stored on the character, and the
// character is never appended to chat history, so prompts cannot accumulate
// from one turn to the next.
func Prompt(required, global string, active Character, context string) string {
	if active.IsDefault() {
		return joinPromptLayers(global, context)
	}
	return joinPromptLayers(required, active.block(), context)
}

func joinPromptLayers(layers ...string) string {
	var b strings.Builder
	for i, layer := range layers {
		if i > 0 {
			layer = strings.TrimSpace(layer)
		}
		if layer == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(layer)
	}
	return b.String()
}

// IsDefault reports whether c is the built-in fallback rather than a stored
// persona. An empty id is treated as the default for defensive callers.
func (c Character) IsDefault() bool {
	return c.ID == "" || c.ID == DefaultID
}

// block renders only the character layer: role first, then rules. It is empty
// for the default character so no character text reaches the model then.
func (c Character) block() string {
	if c.IsDefault() {
		return ""
	}
	name := strings.TrimSpace(c.Name)
	prompt := strings.TrimSpace(c.Prompt)
	rules := cleanRules(c.Rules)
	if name == "" && prompt == "" && len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	if name != "" {
		b.WriteString("Роль персонажа «")
		b.WriteString(name)
		b.WriteString("».")
	} else {
		b.WriteString("Роль персонажа.")
	}
	if prompt != "" {
		b.WriteString("\n")
		b.WriteString(prompt)
	}
	if len(rules) > 0 {
		b.WriteString("\nПравила персонажа:")
		for _, rule := range rules {
			b.WriteString("\n- ")
			b.WriteString(rule)
		}
	}
	// The layer order is fixed, but the character owns conversational identity
	// while technical and safety instructions stay authoritative: a persona
	// must not disable tools, memory search or honest self-description.
	b.WriteString("\nВыбранный персонаж задаёт разговорную роль, самопредставление и ответ на вопрос «кто ты»; в этих вопросах он важнее общего описания ассистента.")
	b.WriteString("\nТехнические инструкции, безопасность, служебные форматы инструментов, памяти и поиска важнее характера и правил персонажа: при конфликте выполняй их.")
	b.WriteString("\nЕсли прямо спрашивают, человек ли ты или ИИ, не притворяйся человеком: честно скажи, что ты ИИ, сохраняя роль.")
	return b.String()
}
