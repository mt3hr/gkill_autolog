// release/ に出来た成果物が、狙ったプラットフォーム向けになっているか確かめる。
//
//   node src/tools/verify_release_artifacts.mjs
//
// クロスコンパイルは設定を1つ間違えるだけで、中身が別プラットフォームの
// バイナリのまま出来上がる。実際、Windows 上で NDK の clang を指定したときに
// Android 向けのつもりが Windows のバイナリ (MZ) になったことがある。
// ファイル名では気づけないので、中身を見て確かめる。

import { closeSync, existsSync, openSync, readSync, statSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..", "..");

/** ELF のマシン種別 (e_machine)。 */
const ELF_MACHINE = {
  0x03: "x86",
  0x28: "arm",
  0x3e: "amd64",
  0xb7: "arm64",
};

const expected = [
  { file: "release/windows_amd64/autolog.exe", format: "pe" },
  { file: "release/linux_amd64/autolog", format: "elf", arch: "amd64" },
  { file: "release/linux_arm64/autolog", format: "elf", arch: "arm64" },
  { file: "release/linux_arm/autolog", format: "elf", arch: "arm" },
  { file: "release/android_arm64/autolog", format: "elf", arch: "arm64" },
  { file: "release/android_apk/gkill_autolog.apk", format: "zip" },
];

/** 先頭のバイトから形式と CPU を読む。 */
function inspect(fullPath) {
  // 判定に要るのは先頭 20 バイトだけ。APK は数十 MB あるので全部は読まない。
  const head = Buffer.alloc(20);
  const fd = openSync(fullPath, "r");
  try {
    readSync(fd, head, 0, head.length, 0);
  } finally {
    closeSync(fd);
  }

  if (head[0] === 0x7f && head.subarray(1, 4).toString() === "ELF") {
    // e_machine は e_ident (16 バイト) と e_type (2 バイト) の後、
    // オフセット 18 からの 2 バイト（リトルエンディアン）。
    return { format: "elf", arch: ELF_MACHINE[head.readUInt16LE(18)] ?? `不明(0x${head.readUInt16LE(18).toString(16)})` };
  }
  if (head[0] === 0x4d && head[1] === 0x5a) return { format: "pe" };
  if (head[0] === 0x50 && head[1] === 0x4b) return { format: "zip" };
  return { format: `不明(${head.subarray(0, 4).toString("hex")})` };
}

let failed = 0;
for (const item of expected) {
  const fullPath = path.join(repoRoot, item.file);
  if (!existsSync(fullPath)) {
    console.log(`  スキップ ${item.file} (作られていない)`);
    continue;
  }

  const actual = inspect(fullPath);
  const sizeMB = (statSync(fullPath).size / 1024 / 1024).toFixed(1);

  const formatOk = actual.format === item.format;
  const archOk = !item.arch || actual.arch === item.arch;

  if (formatOk && archOk) {
    console.log(`  OK   ${item.file} (${actual.format}${actual.arch ? " " + actual.arch : ""}, ${sizeMB} MB)`);
  } else {
    console.log(
      `  NG   ${item.file} : ${item.format}${item.arch ? " " + item.arch : ""} のはずが ` +
        `${actual.format}${actual.arch ? " " + actual.arch : ""}`,
    );
    failed++;
  }
}

if (failed > 0) {
  console.error(`\n${failed} 件が狙ったプラットフォーム向けになっていない。`);
  process.exit(1);
}
console.log("\nすべて狙ったプラットフォーム向けになっている。");
