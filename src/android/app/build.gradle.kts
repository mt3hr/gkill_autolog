import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    // AGP 9 は Kotlin サポートを内蔵しているため kotlin-android プラグインは適用しない
    alias(libs.plugins.android.application)
}

// 署名の設定はリポジトリに置かない。
// 秘密を含むので ~/.gradle/gradle.properties か環境変数から読む。
// 作り方と設定の書き方は documents/reverse/dev-setup.md にある。
val keystoreFile = (findProperty("gkillAutologKeystoreFile") as? String)
    ?: System.getenv("GKILL_AUTOLOG_KEYSTORE_FILE")
val keystorePassword = (findProperty("gkillAutologKeystorePassword") as? String)
    ?: System.getenv("GKILL_AUTOLOG_KEYSTORE_PASSWORD")
val keyAlias0 = (findProperty("gkillAutologKeyAlias") as? String)
    ?: System.getenv("GKILL_AUTOLOG_KEY_ALIAS")
val keyPassword0 = (findProperty("gkillAutologKeyPassword") as? String)
    ?: System.getenv("GKILL_AUTOLOG_KEY_PASSWORD")
val hasSigningConfig = !keystoreFile.isNullOrBlank() && !keystorePassword.isNullOrBlank() &&
    !keyAlias0.isNullOrBlank() && !keyPassword0.isNullOrBlank()

android {
    namespace = "com.mt3hr.gkill_autolog"

    // compileSdk は新しい API を参照できるように上げてよいが、targetSdk は
    // 実機で挙動が変わるため、動きを確かめた版から上げない。
    compileSdk = 37

    defaultConfig {
        applicationId = "com.mt3hr.gkill_autolog"
        minSdk = 26
        targetSdk = 36

        // 通常は build_apk.mjs が package.json の version から渡す。
        // 手で gradlew を叩いたときのために既定値を置く。Android は
        // versionCode の引き下げを拒むので、この既定のまま実機へ入れると
        // 以後まともな版へ入れ替えられなくなる点に注意する。
        versionCode = (findProperty("versionCode") as? String)?.toIntOrNull() ?: 1
        versionName = (findProperty("versionName") as? String) ?: "1.0.0"
    }

    if (hasSigningConfig) {
        signingConfigs {
            create("release") {
                storeFile = file(keystoreFile!!)
                storePassword = keystorePassword
                keyAlias = keyAlias0
                keyPassword = keyPassword0
            }
        }
    }

    buildTypes {
        release {
            // 難読化はしない。落ちたときの記録 (crash.log) をそのまま読めるようにする。
            // proguardFiles の宣言も置かない。isMinifyEnabled = false では使われず、
            // 「効いている」と誤解させるだけなので。
            isMinifyEnabled = false

            // 署名の設定が無ければ、署名なしの APK を黙って作らずに失敗させる。
            // 未署名の APK は端末に入らないので、気づくのは配る直前になる。
            if (hasSigningConfig) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlin {
        compilerOptions {
            jvmTarget.set(JvmTarget.JVM_17)
        }
    }
}

// 署名の設定が無いまま release を組もうとしたら、その場で理由を示して止める。
// package も対象にする。assemble だけだと、署名なしの APK が出来上がってから
// 止まることになり、成果物が中途半端に残る。
tasks.matching { it.name.matches(Regex("(assemble|package|bundle).*Release.*")) }.configureEach {
    doFirst {
        if (!hasSigningConfig) {
            throw GradleException(
                "release の署名設定がありません。~/.gradle/gradle.properties に " +
                    "gkillAutologKeystoreFile / gkillAutologKeystorePassword / " +
                    "gkillAutologKeyAlias / gkillAutologKeyPassword を設定してください " +
                    "(作り方は documents/reverse/dev-setup.md)"
            )
        }
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.material)
    implementation(libs.androidx.activity)
    implementation(libs.androidx.constraintlayout)
    implementation(libs.androidx.work.runtime.ktx)
}
