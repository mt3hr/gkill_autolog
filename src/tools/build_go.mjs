// autolog を1つのプラットフォーム向けにビルドする。
//
//   node src/tools/build_go.mjs <出力ディレクトリ名> <ファイル名>
//
// GOOS / GOARCH / CGO_ENABLED は呼び出し側（npm スクリプト）が渡す。
// 出力は release/<出力ディレクトリ名>/<ファイル名>。
//
// CGO は使わない。SQLite が純 Go の実装なので不要で、
// Windows 上で NDK の clang を指定すると Windows のバイナリが出てしまう。

import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const [target, fileName] = process.argv.slice(2);
if (!target || !fileName) {
  console.error("使い方: node src/tools/build_go.mjs <出力ディレクトリ名> <ファイル名>");
  process.exit(1);
}

const repoRoot = path.resolve(import.meta.dirname, "..", "..");
const moduleDir = path.join(repoRoot, "src", "autolog");
const outDir = path.join(repoRoot, "release", target);
const outFile = path.join(outDir, fileName);

mkdirSync(outDir, { recursive: true });

execFileSync(
  "go",
  ["build", "-trimpath", "-ldflags", "-s -w", "-o", outFile, "./cmd/autolog"],
  { cwd: moduleDir, stdio: "inherit" },
);

console.log(`${target}: ${path.relative(repoRoot, outFile)}`);
