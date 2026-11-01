# AIBox installer for Windows. Runs on your computer (Windows PowerShell 5.1
# or PowerShell 7) and installs or updates the plugin on the speaker over SSH,
# like install.sh does on macOS and Linux:
#
#   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/SileboxUnderfined/aibox-y/main/install.ps1).TrimStart([char]0xFEFF))) -Chat 192.168.88.202
#
# Needs the OpenSSH client (built into Windows 10 1809+ and Windows 11) and tar.exe
# (built in since Windows 10 1803). The speaker-side work is done by setup.sh
# from the package itself. The file is saved with a BOM so that Windows
# PowerShell 5.1 reads the Russian messages as UTF-8.
param(
 [Parameter(Position = 0)][string]$Ip = $env:AIBOX_HOST,
 [switch]$Chat,
 [string]$Version = $env:AIBOX_VERSION,
 [string]$Package,
 [int]$Port = $(if ($env:AIBOX_SSH_PORT) { [int]$env:AIBOX_SSH_PORT } else { 2222 }),
 [switch]$Help
)
$ErrorActionPreference = 'Stop'
# Invoke-WebRequest in Windows PowerShell is very slow while drawing progress.
$ProgressPreference = 'SilentlyContinue'
$repo = 'SileboxUnderfined/aibox-y'

if ($Help) {
 @'
Установка и обновление AIBox на Яндекс Станции по SSH.

  install.ps1 [параметры] <IP колонки>

  -Chat            включить чаты и переключить Алису на AIBox
  -Version vX.Y.Z  поставить конкретный релиз (по умолчанию последний)
  -Package PATH    поставить свою сборку: папку aibox-pkg или архив .tar.gz
  -Port N          SSH-порт колонки (по умолчанию 2222)

Настройки, ключи и чаты в /data/opt/aibox при обновлении сохраняются.
Откат на колонке: sh <папка плагина>/aiboxctl.sh rollback
'@
 return
}
# throw, not exit: the one-liner runs inside the user's own PowerShell window.
function Fail([string]$msg) { throw "aibox: $msg" }
function Say([string]$msg) { Write-Host "aibox: $msg" }

$ssh = Get-Command ssh -ErrorAction SilentlyContinue
if (-not $ssh) {
 Fail 'нет ssh. Включи «Клиент OpenSSH»: Параметры > Система > Дополнительные компоненты'
}
if (-not $Ip) { $Ip = Read-Host 'IP колонки' }
if (-not $Ip) { Fail 'IP не указан' }
if ($Ip -like '*@*') { $target = $Ip } else { $target = "root@$Ip" }
$addr = $Ip.Substring($Ip.IndexOf('@') + 1)

$work = Join-Path ([IO.Path]::GetTempPath()) ('aibox-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
$archive = Join-Path $work 'aibox.tar.gz'
$oldEncoding = [Console]::OutputEncoding
try {
 if ($Package) {
  if (Test-Path -LiteralPath $Package -PathType Container) {
   if (-not (Test-Path -LiteralPath (Join-Path $Package 'plugin.toml'))) {
    Fail "$Package не похожа на aibox-pkg (нет plugin.toml)"
   }
   if (-not (Get-Command tar -ErrorAction SilentlyContinue)) { Fail 'нет tar.exe, нужен Windows 10 1803 или новее' }
   # The speaker side expects the folder to be called aibox-pkg.
   $src = Join-Path $work 'src'
   New-Item -ItemType Directory -Path $src | Out-Null
   Copy-Item -LiteralPath $Package -Destination (Join-Path $src 'aibox-pkg') -Recurse
   & tar -czf $archive -C $src aibox-pkg
   if ($LASTEXITCODE -ne 0) { Fail "не удалось упаковать $Package" }
  } elseif (Test-Path -LiteralPath $Package -PathType Leaf) {
   Copy-Item -LiteralPath $Package -Destination $archive
  } else {
   Fail "нет такого файла или папки: $Package"
  }
  Say "ставлю свою сборку $Package"
 } else {
  # Windows PowerShell 5.1 may still default to TLS 1.0, which GitHub refuses.
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  if (-not $Version) {
   try {
    $Version = (Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$repo/releases/latest").tag_name
   } catch {
    Fail 'на GitHub ещё нет релизов; собери сам (make package) и запусти с -Package dist\aibox-pkg'
   }
  }
  $base = "https://github.com/$repo/releases/download/$Version"
  $name = "aibox-$Version-linux-arm64.tar.gz"
  $sums = Join-Path $work 'SHA256SUMS'
  Say "скачиваю $name"
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile $archive } catch { Fail "не удалось скачать $base/$name" }
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums } catch { Fail 'не удалось скачать SHA256SUMS' }
  $pattern = "^([0-9a-fA-F]{64}) +\*?$([regex]::Escape($name))$"
  $line = Get-Content -LiteralPath $sums | Where-Object { $_ -match $pattern } | Select-Object -First 1
  if (-not $line -or $line -notmatch $pattern) { Fail "в SHA256SUMS нет $name" }
  $want = $Matches[1]
  $got = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
  if ($got -ne $want.ToUpperInvariant()) { Fail "контрольная сумма $name не совпала" }
 }

 Say "подключаюсь к $target (порт $Port)"
 $flags = ''
 if ($Chat) { $flags = ' --chat' }
 # One connection: the archive goes in on stdin and unpacks to /tmp (RAM, not /data).
 $remote = "cd /tmp && rm -rf aibox-pkg && gzip -dc | tar xf - && AIBOX_HOST='$addr' sh aibox-pkg/setup.sh$flags; rc=`$?; rm -rf /tmp/aibox-pkg; exit `$rc"
 # The speaker answers in UTF-8.
 [Console]::OutputEncoding = [Text.Encoding]::UTF8
 # PowerShell pipes text, not bytes, so the archive is fed to ssh's stdin by hand.
 # ssh asks for a password or host key on the console, not on stdin.
 $psi = New-Object Diagnostics.ProcessStartInfo
 $psi.FileName = $ssh.Source
 $psi.Arguments = "-p $Port $target `"$remote`""
 $psi.UseShellExecute = $false
 $psi.RedirectStandardInput = $true
 $proc = [Diagnostics.Process]::Start($psi)
 $in = [IO.File]::OpenRead($archive)
 try {
  try { $in.CopyTo($proc.StandardInput.BaseStream) } catch { }
  try { $proc.StandardInput.Close() } catch { }
 } finally {
  $in.Close()
 }
 $proc.WaitForExit()
 if ($proc.ExitCode -ne 0) { Fail "установка на колонке не удалась (код $($proc.ExitCode))" }
} finally {
 [Console]::OutputEncoding = $oldEncoding
 Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
