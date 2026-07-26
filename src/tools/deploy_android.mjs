// Android (arm64) 向けの autolog を Dropbox へ置く。
//
//   node src/tools/deploy_android.mjs
//
// Termux の gkill_server と同じ配り方。
// 端末側は ~/.termux/tasker/update_autolog.sh が
// dropbox:/autolog を go/bin へ落として使う。
//
// 先に npm run build_android_arm64 でビルドしておくこと。

import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..", "..");
const binary = path.join(repoRoot, "release", "android_arm64", "autolog");

if (!existsSync(binary)) {
  console.error(`ビルドされていない: ${binary}`);
  console.error("先に npm run build_android_arm64 を実行してください。");
  process.exit(1);
}

// 中身が本当に arm64 の ELF か確かめてから配る。
execFileSync("node", [path.join(repoRoot, "src", "tools", "verify_release_artifacts.mjs")], {
  stdio: "inherit",
});

// hbg copy に上書きの指定は無い。同名なら更新される。
execFileSync("hbg", ["copy", `local:${binary}`, "dropbox:/"], { stdio: "inherit" });

console.log("\ndropbox:/autolog に置いた。端末側での取り込み:");
console.log("  ~/.termux/tasker/update_autolog.sh");
console.log("  ~/.termux/tasker/autolog.sh");
