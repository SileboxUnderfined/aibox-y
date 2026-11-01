#!/bin/sh
# Prints the GitHub release description for a tag: the tag's section from
# CHANGELOG.md, then the install instructions. Used by .github/workflows/release.yml.
#
#   packaging/release/notes.sh v0.1.1 [binary size] > notes.md
set -eu
tag=$1
size=${2:-}
repo=${GITHUB_REPOSITORY:-SileboxUnderfined/aibox-y}
changelog=$(dirname "$0")/../../CHANGELOG.md

# Lines after "## <tag>" up to the next "## " heading.
changes=$(awk -v tag="$tag" '
  /^## / { if (found) exit; found = ($2 == tag); next }
  found { print }
' "$changelog" | sed -e '/./,$!d')

if [ -z "$changes" ]; then
  echo "::warning::CHANGELOG.md has no section for $tag; listing commits instead" >&2
  prev=$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)
  changes=$(git log --no-merges --format='- %s' ${prev:+"$prev.."}"$tag")
fi

cat <<NOTES
## Что нового

$changes

## Установка и обновление

Одна команда на компьютере ставит AIBox или обновляет его до $tag. Нужен SSH-доступ к колонке; вместо \`192.168.88.202\` подставьте её IP. Настройки, ключи и чаты в \`/data/opt/aibox\` при обновлении сохраняются.

**macOS и Linux:**

\`\`\`bash
curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sh -s -- --version $tag --chat 192.168.88.202
\`\`\`

**Windows (PowerShell 5.1 или 7):**

\`\`\`powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/$repo/main/install.ps1).TrimStart([char]0xFEFF))) -Version $tag -Chat 192.168.88.202
\`\`\`

\`--chat\` / \`-Chat\` сразу включает чаты и переключает Алису на AIBox; без него Алиса продолжает ходить в Яндекс напрямую. Остальные параметры описаны в [README](https://github.com/$repo#-установка).

Без SSH: файл \`.yap\` можно загрузить через веб-панель Luna или старого YAM (нужна галочка «Разрешить бинарники плагинов»).

Плагин для Станции Мини 2 (linux/arm64)${size:+, бинарник после распаковки $size}. Проверка файлов: \`sha256sum -c SHA256SUMS --ignore-missing\`. Откат на колонке: \`sh <папка плагина>/aiboxctl.sh rollback\`.
NOTES
