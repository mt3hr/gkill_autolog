// Android の収集アプリをビルドする。
//
//   node src/tools/build_apk.mjs
//
// バージョンは package.json の version から決める。
// versionCode は major*10000 + minor*100 + patch（1.0.0 なら 10000）。
// Android は versionCode の引き下げを拒むので、上げ忘れると入れ替えられなくなる。
//
// 出力は release/android_apk/gkill_autolog.apk。
//
// release ビルドで作る。debug ビルドは debuggable なうえ、署名鍵が
// 機械ごとの debug keystore になるため、別の機械で組み直すと署名不一致で
// 上書きできず、アンインストール（＝未書き出しの記録の喪失）を強いられる。
// 署名の設定は ~/.gradle/gradle.properties か環境変数から読む
// （リポジトリには置かない。作り方は documents/reverse/dev-setup.md）。

import { execSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..", "..");
const androidDir = path.join(repoRoot, "src", "android");
const outDir = path.join(repoRoot, "release", "android_apk");

const { version } = JSON.parse(readFileSync(path.join(repoRoot, "package.json"), "utf8"));

// 1.0.0-dev のような接尾辞は versionCode の計算から外す。
const [major = 0, minor = 0, patch = 0] = version.replace(/-.*$/, "").split(".").map(Number);
const versionCode = major * 10000 + minor * 100 + patch;

mkdirSync(outDir, { recursive: true });

// gradlew は Unix のシェルスクリプト。CRLF だと実行できないので均しておく。
// .gitattributes で LF に固定しているが、手元で変わっていることがある。
const gradlew = path.join(androidDir, "gradlew");
writeFileSync(gradlew, readFileSync(gradlew, "utf8").replace(/\r\n/g, "\n"));
try {
  chmodSync(gradlew, 0o755);
} catch {
  // Windows では chmod が効かないが、gradlew.bat を使うので問題ない。
}

// Windows では絶対パスで叩く。cwd を指定しても cmd が
// カレントディレクトリの gradlew.bat を見つけてくれないことがある。
const command =
  process.platform === "win32" ? `"${path.join(androidDir, "gradlew.bat")}"` : "./gradlew";

execSync(
  `${command} assembleRelease -PversionName=${version} -PversionCode=${versionCode}`,
  { cwd: androidDir, stdio: "inherit" },
);

const built = path.join(androidDir, "app", "build", "outputs", "apk", "release", "app-release.apk");
const released = path.join(outDir, "gkill_autolog.apk");
copyFileSync(built, released);

console.log(`android_apk: ${path.relative(repoRoot, released)} (${version} / ${versionCode})`);
