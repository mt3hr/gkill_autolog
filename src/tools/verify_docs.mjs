// 資料の機械検査。npm run verify_docs（npm test の先頭）で回る。
//
//   node src/tools/verify_docs.mjs          検査する
//   node src/tools/verify_docs.mjs --list   実測メトリクスを出す
//
// gkill 本体の src/tools/verify_docs.mjs から、このリポジトリに実体のある検査を移植した。
// ADR とマニュアルの検査は移植していない（documents/adr/ もマニュアルも無く、
// 実体の無い検査は空回りするため）。作るときに本体から checkADR を移植すること。
//
// 検査を変えたら、わざと壊して落ちることを手で確認する。この検査自身に単体テストは無い。

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'

const ROOT = path.resolve(import.meta.dirname, '..', '..') // src/tools/ → リポジトリルート

const abs = (rel) => path.join(ROOT, rel)
const exists = (rel) => fs.existsSync(abs(rel))
const readText = (rel) => fs.readFileSync(abs(rel), 'utf8')
// 作業ツリーは CRLF。バイト計測も正規表現も、必ずこれを通してから行う。
const normalizeLF = (s) => s.replace(/\r\n/g, '\n')

function listFiles(dir, filter) {
  if (!exists(dir)) return []
  return fs.readdirSync(abs(dir)).filter(filter).sort()
}

function listFilesRec(dir, filter) {
  const out = []
  if (!exists(dir)) return out
  const walk = (current) => {
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const p = path.join(current, entry.name)
      if (entry.isDirectory()) {
        if (SKIP_DIRS.has(entry.name)) continue
        walk(p)
        continue
      }
      if (filter(entry.name)) out.push(p)
    }
  }
  walk(abs(dir))
  return out
}

const errors = []
const warnings = []
const err = (message) => errors.push(message)
const warn = (message) => warnings.push(message)

// 走査から外すディレクトリ（生成物・依存・IDE）
const SKIP_DIRS = new Set([
  'node_modules', '.git', 'release', 'build', '.gradle', '.idea', '.kotlin', '.cxx',
])

// ─────────────────────────────────────────────────────────────
// 1. 実測メトリクス
// ─────────────────────────────────────────────────────────────
function countMatches(files, re) {
  let n = 0
  for (const p of files) {
    const matches = fs.readFileSync(p, 'utf8').match(re)
    if (matches) n += matches.length
  }
  return n
}

function computeMetrics() {
  const goAll = listFilesRec('src/autolog', (f) => f.endsWith('.go'))
  const goTestFiles = goAll.filter((p) => p.endsWith('_test.go'))
  const pkg = JSON.parse(readText('package.json'))
  return {
    go_files: goAll.length - goTestFiles.length,
    go_test_files: goTestFiles.length,
    go_tests: countMatches(goTestFiles, /^func Test[A-Za-z0-9_]*\(/gm),
    internal_packages: fs.readdirSync(abs('src/autolog/internal'), { withFileTypes: true })
      .filter((e) => e.isDirectory()).length,
    chrome_ext_tests: listFiles('src/chrome_ext', (f) => f.endsWith('.test.mjs')).length,
    powershell_scripts: listFiles('src/scripts', (f) => f.endsWith('.ps1')).length,
    reverse_docs: listFiles('documents/reverse', (f) => f.endsWith('.md')).length,
    npm_scripts: Object.keys(pkg.scripts).length,
    skills: skillNames().length,
  }
}

// ─────────────────────────────────────────────────────────────
// 2. 件数アサーション
//
//   資料に数字を書いたらここへ足す。照合は素の部分文字列一致なので、
//   語句が移った瞬間に赤くなる（それが狙い）。
// ─────────────────────────────────────────────────────────────
function buildCountAssertions(m) {
  const list = []
  const add = (file, text, label) => list.push({ file, text, label })

  add('CLAUDE.md', `（${m.skills}スキル）`, '規約スキルの本数')
  add('AGENTS.md', 'Go 1.26 以上', 'Go の最低バージョン')

  return list
}

function checkCounts(m) {
  for (const { file, text, label } of buildCountAssertions(m)) {
    if (!exists(file)) { err(`件数アサーションの対象ファイルが無い: ${file}`); continue }
    if (!normalizeLF(readText(file)).includes(text)) {
      err(`件数の記述が実測と合わない: ${file} に「${text}」が無い（${label}）`)
    }
  }
}

// ─────────────────────────────────────────────────────────────
// 3. 検査対象の Markdown
//
//   **検査対象の資料ジャンルを増やすときは、必ずこの関数へ足すこと。**
//   ここが唯一の入口なので、足し忘れるとリンク切れもゴーストファイル名も素通りする。
// ─────────────────────────────────────────────────────────────
function docMarkdownFiles() {
  const out = []
  for (const f of listFiles('documents/reverse', (f2) => f2.endsWith('.md'))) {
    out.push('documents/reverse/' + f)
  }
  for (const rel of ['README.md', 'AGENTS.md', 'CLAUDE.md']) {
    if (exists(rel)) out.push(rel)
  }
  for (const p of listFilesRec('.claude/skills', (f) => f.endsWith('.md'))) {
    out.push(path.relative(ROOT, p).split(path.sep).join('/'))
  }
  for (const p of listFilesRec('src', (f) => f === 'README.md')) {
    out.push(path.relative(ROOT, p).split(path.sep).join('/'))
  }
  return out
}

function stripFencedBlocks(text) {
  return text.replace(/^\s*```[\s\S]*?^\s*```/gm, '\n')
}

// ─────────────────────────────────────────────────────────────
// 4. リンク切れ
// ─────────────────────────────────────────────────────────────
function checkLinks() {
  const linkRe = /\]\(([^)]+)\)/g
  for (const rel of docMarkdownFiles()) {
    const dir = path.dirname(rel)
    const text = readText(rel)
    let mt
    while ((mt = linkRe.exec(text)) !== null) {
      const target = mt[1].trim()
      if (/^(https?:)?\/\//.test(target) || target.startsWith('#') || target.startsWith('mailto:')) continue
      const hashIdx = target.indexOf('#')
      const filePart = hashIdx >= 0 ? target.slice(0, hashIdx) : target
      if (!filePart) continue // 同一ファイル内アンカー
      if (!exists(path.join(dir, filePart))) err(`リンク切れ: ${rel} → ${target}`)
    }
  }
}

// ─────────────────────────────────────────────────────────────
// 5. 参照パスの実在（警告のみ）
//
//   資料はモジュール相対（internal/normalize、cmd/autolog/cmd_import.go）でも書く。
//   ルート基準だけで解決すると軒並み未検出になるので、3つの基準で順に探す。
// ─────────────────────────────────────────────────────────────
const PATH_BASES = ['', 'src/autolog/', 'src/']
// このリポジトリに実在しない外部のパス。gkill 本体のソースを名指しする箇所がある。
const EXTERNAL_PATH_PREFIXES = ['src/server/']

function resolvesUnderAnyBase(token) {
  return PATH_BASES.some((base) => exists(base + token))
}

function checkPaths() {
  const codeRe = /`([^`\n]+)`/g
  const seen = new Set()
  for (const rel of docMarkdownFiles()) {
    const text = stripFencedBlocks(readText(rel))
    let mt
    while ((mt = codeRe.exec(text)) !== null) {
      const tok = mt[1].trim()
      if (!/^(src|documents|internal|cmd|schema)\/[\w./-]+$/.test(tok)) continue
      if (tok.includes('*')) continue
      if (!/\.\w+$/.test(tok) && !tok.endsWith('/')) continue
      const key = rel + '::' + tok
      if (seen.has(key)) continue
      seen.add(key)
      if (EXTERNAL_PATH_PREFIXES.some((pre) => tok.startsWith(pre))) continue
      if (!resolvesUnderAnyBase(tok)) warn(`参照パス未検出（要確認）: ${rel} → ${tok}`)
    }
  }
}

// ─────────────────────────────────────────────────────────────
// 6. 資料に載っているファイル名の実在
//
//   件数だけを検査していると「数は合っているのに一覧は古い」が通り抜ける。
//   ASCII ツリーや表セルに書かれた素のファイル名を拾い、
//   同名のファイルがリポジトリのどこにも無ければ落とす。
// ─────────────────────────────────────────────────────────────
const DOC_FILENAME_EXTENSIONS = ['go', 'kt', 'js', 'mjs', 'ps1']
const DOC_FILENAME_PLACEHOLDER = /^_|xxx|yyy|zzz/
// このリポジトリに実在しないが、名指しする必要がある外部のファイル名。
// gkill 本体と Go 標準ライブラリのもの。増やすときは必ず理由を書くこと。
const EXTERNAL_FILENAMES = new Set([
  'Node.js',                              // 「Node.js」の誤検出（js を拡張子に含めたため）
  'zoneinfo_android.go',                  // Go 標準ライブラリ。time.Local が UTC 固定になる出どころ
  'handle_add_urlog.go',                  // gkill 本体。add_urlog が対象URLを再取得する出どころ
  'gkill_server_api_rate_limit.go',       // gkill 本体。ログインの回数制限の出どころ
  'gps_log_repository_gpx_dir_impl.go',   // gkill 本体。GPX を取り込む側
  'error_codes.go',                       // gkill 本体。認証エラーコードの定義
  'window.go',                            // 実在するが normalize 配下。下の basenames で拾われる
])

function collectRepositoryBasenames() {
  const names = new Set()
  const walk = (dir) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        if (SKIP_DIRS.has(entry.name)) continue
        walk(path.join(dir, entry.name))
        continue
      }
      names.add(entry.name)
    }
  }
  walk(ROOT)
  return names
}

function checkDocFilenames() {
  const basenames = collectRepositoryBasenames()
  const fileRe = new RegExp(
    `(?<![\\w./-])([A-Za-z0-9_][\\w.-]*\\.(?:${DOC_FILENAME_EXTENSIONS.join('|')}))(?![\\w-])`, 'g')

  const missing = new Map()
  for (const rel of docMarkdownFiles()) {
    for (const mt of readText(rel).matchAll(fileRe)) {
      const name = mt[1]
      if (DOC_FILENAME_PLACEHOLDER.test(name)) continue
      if (EXTERNAL_FILENAMES.has(name)) continue
      if (basenames.has(name)) continue
      if (!missing.has(name)) missing.set(name, new Set())
      missing.get(name).add(rel)
    }
  }
  for (const name of [...missing.keys()].sort()) {
    err(`資料に載っているファイルが実在しない: ${name}（${[...missing.get(name)].sort().join(', ')}）`)
  }
}

// ─────────────────────────────────────────────────────────────
// 7. Mermaid ブロックの軽量検証
// ─────────────────────────────────────────────────────────────
function checkMermaid() {
  const dir = 'documents/reverse'
  const known = ['graph', 'flowchart', 'sequenceDiagram', 'classDiagram',
    'stateDiagram', 'stateDiagram-v2', 'erDiagram', 'journey', 'gantt',
    'pie', 'gitGraph', 'mindmap', 'timeline', 'quadrantChart']
  const blockRe = /```mermaid\r?\n([\s\S]*?)```/g
  for (const f of listFiles(dir, (f2) => f2.endsWith('.md'))) {
    const rel = dir + '/' + f
    const text = readText(rel)
    let mt
    let idx = 0
    while ((mt = blockRe.exec(text)) !== null) {
      idx++
      const body = mt[1].trim()
      if (!body) { err(`Mermaid 空ブロック: ${rel} #${idx}`); continue }
      const firstLine = body.split(/\r?\n/)[0].trim()
      if (!known.some((k) => firstLine.startsWith(k))) {
        warn(`Mermaid 図種別が不明（要確認）: ${rel} #${idx} → 「${firstLine.slice(0, 40)}」`)
      }
    }
  }
}

// ─────────────────────────────────────────────────────────────
// 8. 索引への掲載漏れ
//
//   資料を足したのに documents/reverse/README.md から辿れないと、
//   その資料は誰にも読まれないまま古びる。
// ─────────────────────────────────────────────────────────────
function checkIndexCoverage() {
  const indexRel = 'documents/reverse/README.md'
  if (!exists(indexRel)) { err(`資料の索引が無い: ${indexRel}`); return }
  const index = normalizeLF(readText(indexRel))
  for (const f of listFiles('documents/reverse', (f2) => f2.endsWith('.md') && f2 !== 'README.md')) {
    if (!index.includes(`(${f})`)) {
      err(`資料が索引に載っていない: documents/reverse/${f}（${indexRel} からリンクすること）`)
    }
  }
}

// ─────────────────────────────────────────────────────────────
// 9. AI 向け資料層（AGENTS.md / スキル / 他AI入口）
// ─────────────────────────────────────────────────────────────
const SKILLS_DIR = '.claude/skills'
// LF 正規化後の実測 + 余裕。上限は「気付かせる装置」であって「許容量」ではない。
// **当たったら上限を上げるのではなく、中身をスキルへ落とすこと。**
const AGENTS_MD_MAX_BYTES = 19000
const CLAUDE_MD_MAX_LINES = 40
const ENTRYPOINTS = ['.github/copilot-instructions.md', '.cursor/rules/autolog.mdc']

function skillNames() {
  return listFiles(SKILLS_DIR, (d) => exists(`${SKILLS_DIR}/${d}/SKILL.md`)).sort()
}

function checkAgentEntrypoints() {
  if (!exists('AGENTS.md')) { err('AGENTS.md が無い（AI エージェント共通の入口）'); return }
  const agents = normalizeLF(readText('AGENTS.md'))
  const bytes = Buffer.byteLength(agents, 'utf8')
  if (bytes > AGENTS_MD_MAX_BYTES) {
    err(`AGENTS.md が ${bytes} バイト（上限 ${AGENTS_MD_MAX_BYTES}）。` +
      '上限を上げるのではなく、領域別の内容を .claude/skills/ へ移してルーティング表に載せること')
  }
  if (!agents.includes('<!-- ROUTING-TABLE:BEGIN')) err('AGENTS.md にルーティング表のマーカーが無い')

  if (!exists('CLAUDE.md')) { err('CLAUDE.md が無い（Claude Code の入口）'); return }
  const claude = normalizeLF(readText('CLAUDE.md'))
  if (!/^@AGENTS\.md\s*$/m.test(claude)) {
    err('CLAUDE.md に `@AGENTS.md` の行が無い（Claude Code に AGENTS.md が読み込まれない）')
  }
  const claudeLines = claude.split('\n').length
  if (claudeLines > CLAUDE_MD_MAX_LINES) {
    err(`CLAUDE.md が ${claudeLines} 行（上限 ${CLAUDE_MD_MAX_LINES}）。` +
      '規約の正本は AGENTS.md と .claude/skills/。CLAUDE.md は入口だけを持つこと')
  }

  // 他AIツールの入口は導線だけを持つ。規約本文の複製は必ずドリフトする
  for (const rel of ENTRYPOINTS) {
    if (!exists(rel)) { err(`AI 入口ファイルが無い: ${rel}`); continue }
    const pointer = normalizeLF(readText(rel))
    if (!pointer.includes('AGENTS.md')) err(`AI 入口が AGENTS.md を指していない: ${rel}`)
    if (Buffer.byteLength(pointer, 'utf8') > 4096) err(`AI 入口が大きすぎる: ${rel}（導線だけにする）`)
    if (/してはいけない|してはならない/.test(pointer)) {
      err(`AI 入口に規約本文が書かれている: ${rel}（正本は AGENTS.md と .claude/skills/）`)
    }
  }
  const geminiRel = '.gemini/settings.json'
  if (!exists(geminiRel)) {
    err(`AI 入口ファイルが無い: ${geminiRel}`)
  } else {
    let gemini
    try {
      gemini = JSON.parse(readText(geminiRel))
    } catch {
      err(`${geminiRel} が JSON として読めない`)
    }
    if (gemini && !(gemini.contextFileName || []).includes('AGENTS.md')) {
      err(`${geminiRel} の contextFileName に AGENTS.md が無い（Gemini CLI が規約を読まない）`)
    }
  }
}

function checkSkills() {
  const names = skillNames()
  if (!names.length) {
    err(`規約スキルが1つも見つからない: ${SKILLS_DIR}/*/SKILL.md` +
      '（.gitignore が /.claude/* + !/.claude/skills/ になっているか確認。' +
      'ディレクトリごと無視すると新しいクローンに存在せず、検査が静かにゼロ件になる）')
    return
  }
  const agents = exists('AGENTS.md') ? normalizeLF(readText('AGENTS.md')) : ''
  const tableMatch = agents.match(/<!-- ROUTING-TABLE:BEGIN[\s\S]*?<!-- ROUTING-TABLE:END -->/)
  const table = tableMatch ? tableMatch[0] : ''
  const linked = new Set()
  for (const mt of table.matchAll(/\]\((\.claude\/skills\/[\w-]+\/SKILL\.md)\)/g)) linked.add(mt[1])

  for (const name of names) {
    const rel = `${SKILLS_DIR}/${name}/SKILL.md`
    const text = normalizeLF(readText(rel))
    const fm = text.match(/^---\n([\s\S]*?)\n---\n/)
    if (!fm) { err(`SKILL.md に frontmatter が無い: ${rel}`); continue }
    const nameLine = fm[1].match(/^name:\s*(.+?)\s*$/m)
    if (!nameLine || nameLine[1] !== name) {
      err(`SKILL.md の name がディレクトリ名と違う: ${rel} → 「${nameLine ? nameLine[1] : '(無し)'}」`)
    }
    // description は二重引用符でくくった1物理行（オンデマンド発動の唯一の手がかり）
    const descLine = fm[1].match(/^description:\s*"(.+)"\s*$/m)
    if (!descLine) {
      err(`SKILL.md の description が無いか、二重引用符1行の形式でない: ${rel}`)
    } else {
      const d = descLine[1]
      if (d.length < 80) err(`description が短すぎて発動精度が出ない: ${rel}（${d.length}字）`)
      if (d.length > 1024) err(`description が長すぎる（常時コンテキストを食う）: ${rel}（${d.length}字）`)
      if (!/(src\/|\.claude\/|documents\/|package\.json|AGENTS\.md|CLAUDE\.md|\.(go|kt|js|mjs|ps1)\b)/.test(d)) {
        err(`description に発動の手がかり（パスやファイル名）が無い: ${rel}`)
      }
    }
    if (!linked.has(rel)) {
      err(`スキルが AGENTS.md のルーティング表に無い: ${rel}` +
        '（表に行が無いスキルは、パス連動のスキル機構を持たないエージェントから永遠に読まれない）')
    }
    // 1スキル = SKILL.md 1ファイル。補助 .md の散在は「索引に載らず読まれない資料」になる
    for (const f of listFiles(`${SKILLS_DIR}/${name}`, (f2) => f2.endsWith('.md') && f2 !== 'SKILL.md')) {
      err(`スキルに SKILL.md 以外の .md がある: ${SKILLS_DIR}/${name}/${f}（1スキル=1ファイル。内容は SKILL.md へ）`)
    }
  }
  for (const p of linked) {
    const dirName = p.split('/')[2]
    if (!names.includes(dirName)) err(`ルーティング表にあるスキルが実在しない: ${p}`)
  }
}

// ソース内アンカーコメント（規約スキルへの参照）の実在検査。
// コメント内の参照は checkLinks に載らないので、スキルの改名・削除で静かに古びる。
function checkSkillAnchors() {
  const re = /\.claude\/skills\/[\w-]+\/SKILL\.md/g
  for (const p of listFilesRec('src', (f) => /\.(go|kt|js|mjs|ps1)$/.test(f))) {
    for (const mt of fs.readFileSync(p, 'utf8').matchAll(re)) {
      if (!exists(mt[0])) {
        err('ソースのアンカーコメントが指すスキルが実在しない: ' +
          `${path.relative(ROOT, p).split(path.sep).join('/')} → ${mt[0]}`)
      }
    }
  }
}

// .gitignore がスキルを追跡できる形になっているか。
// 裸の `.claude/` へ戻してもローカルではファイルが残るので気づけず、
// 新しいクローンと CI でだけ「スキルが1本も無い」状態になる。
function checkGitignoreSkills() {
  const rel = '.gitignore'
  if (!exists(rel)) { err('.gitignore が無い'); return }
  const lines = normalizeLF(readText(rel)).split('\n').map((l) => l.trim())
  if (lines.includes('.claude/') || lines.includes('/.claude') || lines.includes('.claude')) {
    err('.gitignore に裸の `.claude/` 行がある。' +
      'git はネガティブパターンで親ディレクトリの除外を打ち消せないので、' +
      '`/.claude/*` と `!/.claude/skills/` の2行組にすること')
  }
  for (const needed of ['/.claude/*', '!/.claude/skills/']) {
    if (!lines.includes(needed)) err(`.gitignore に \`${needed}\` の行が無い（規約スキルが追跡されない）`)
  }
}

// 資料に出てくる `npm run <name>` が package.json に実在するか。
function checkNpmScripts() {
  const scripts = new Set(Object.keys(JSON.parse(readText('package.json')).scripts))
  const re = /npm run ([a-z][\w:-]*)/g
  const missing = new Map()
  for (const rel of docMarkdownFiles()) {
    for (const mt of readText(rel).matchAll(re)) {
      if (scripts.has(mt[1])) continue
      if (!missing.has(mt[1])) missing.set(mt[1], new Set())
      missing.get(mt[1]).add(rel)
    }
  }
  for (const name of [...missing.keys()].sort()) {
    err(`資料が指す npm スクリプトが無い: npm run ${name}（${[...missing.get(name)].sort().join(', ')}）`)
  }
}

// 個人情報・実環境情報の混入検査（AGENTS.md「AI エージェントへの約束」の機械化）。
// パターンで表せない固有の NG 語は、それ自体をコミットすると本末転倒なので、
// gitignore 済みの verify_docs_personal_ngwords.local.txt（1行1語）に置く。
function checkPersonalInfo() {
  const patterns = [
    [/[A-Za-z]:\\+Users\\+(?![〈<]|user(?:name)?\b)[A-Za-z0-9]/, 'Windows のユーザープロファイル実パス'],
    [/\/(?:home|Users)\/(?!user\/|〈|<)[a-z0-9_-]{3,}\//, 'ホームディレクトリの実パス'],
    [/[A-Za-z0-9._%+-]+@(?!example\.)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/, 'メールアドレス'],
  ]
  const ngFile = 'verify_docs_personal_ngwords.local.txt'
  const ngWords = exists(ngFile)
    ? normalizeLF(readText(ngFile)).split('\n').map((w) => w.trim()).filter(Boolean)
    : []
  for (const rel of docMarkdownFiles()) {
    const text = normalizeLF(readText(rel))
    for (const [re, label] of patterns) {
      const mt = text.match(re)
      if (mt) {
        err(`個人情報の疑い（${label}）: ${rel} → 「${mt[0].slice(0, 40)}」` +
          '（$HOME や 〈ユーザー名〉 のプレースホルダに置き換えること）')
      }
    }
    for (const w of ngWords) {
      if (text.includes(w)) err(`個人情報の疑い（ローカル NG 語）: ${rel} に「${w}」`)
    }
  }
}

// ─────────────────────────────────────────────────────────────
// メイン
// ─────────────────────────────────────────────────────────────
function main() {
  const m = computeMetrics()

  if (process.argv.includes('--list')) {
    console.log('実測メトリクス:')
    console.log(JSON.stringify(m, null, 2))
    return
  }

  checkCounts(m)
  checkLinks()
  checkPaths()
  checkDocFilenames()
  checkMermaid()
  checkIndexCoverage()
  checkSkills()
  checkAgentEntrypoints()
  checkGitignoreSkills()
  checkNpmScripts()
  checkSkillAnchors()
  checkPersonalInfo()

  for (const w of warnings) console.warn('WARN ' + w)
  if (errors.length) {
    for (const e of errors) console.error('NG   ' + e)
    console.error(`\n資料の検査に失敗しました（エラー ${errors.length} 件 / 警告 ${warnings.length} 件）`)
    process.exit(1)
  }
  console.log(`資料の検査を通過しました（スキル ${m.skills} 本 / 警告 ${warnings.length} 件）`)
}

main()
