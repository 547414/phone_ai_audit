#!/bin/zsh
# Build release APK using optional settings from ../config.env.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config.env}"

if [[ -f "$CONFIG_FILE" ]]; then
  set -a
  source "$CONFIG_FILE"
  set +a
fi

if [[ -n "${ANDROID_JAVA_HOME:-}" ]]; then
  export JAVA_HOME="$ANDROID_JAVA_HOME"
elif [[ -z "${JAVA_HOME:-}" ]]; then
  if [[ -d "/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home" ]]; then
    export JAVA_HOME="/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home"
  elif command -v /usr/libexec/java_home >/dev/null 2>&1; then
    export JAVA_HOME="$(/usr/libexec/java_home -v 17 2>/dev/null || /usr/libexec/java_home)"
  fi
fi

if [[ -z "${ANDROID_HOME:-}" ]]; then
  if [[ -f "$SCRIPT_DIR/local.properties" ]]; then
    ANDROID_HOME="$(sed -n 's/^sdk\.dir=//p' "$SCRIPT_DIR/local.properties" | head -n 1)"
  elif [[ -d "/opt/homebrew/share/android-commandlinetools" ]]; then
    ANDROID_HOME="/opt/homebrew/share/android-commandlinetools"
  elif [[ -d "$HOME/Library/Android/sdk" ]]; then
    ANDROID_HOME="$HOME/Library/Android/sdk"
  fi
fi

if [[ -n "${ANDROID_HOME:-}" ]]; then
  export ANDROID_HOME
  export ANDROID_SDK_ROOT="$ANDROID_HOME"
fi

if [[ -n "${JAVA_HOME:-}" ]]; then
  export PATH="$JAVA_HOME/bin:$PATH"
fi
if [[ -n "${ANDROID_HOME:-}" ]]; then
  export PATH="$ANDROID_HOME/platform-tools:$PATH"
fi
export PATH="/opt/homebrew/bin:$PATH"

PROXY_OPTS=()
if [[ -n "${ANDROID_PROXY_HOST:-}" && -n "${ANDROID_PROXY_PORT:-}" ]]; then
  PROXY_OPTS=(
    "-Dhttp.proxyHost=${ANDROID_PROXY_HOST}"
    "-Dhttp.proxyPort=${ANDROID_PROXY_PORT}"
    "-Dhttps.proxyHost=${ANDROID_PROXY_HOST}"
    "-Dhttps.proxyPort=${ANDROID_PROXY_PORT}"
  )
fi

cd "$SCRIPT_DIR"
if [[ ! -x "./gradlew" ]]; then
  gradle --no-daemon wrapper --gradle-version 8.11.1 --distribution-type bin "${PROXY_OPTS[@]}"
fi

./gradlew --no-daemon assembleRelease "${PROXY_OPTS[@]}"

VERSION_NAME="$(sed -n 's/.*versionName = "\([^"]*\)".*/\1/p' "$SCRIPT_DIR/app/build.gradle.kts" | head -n 1)"
VERSION_NAME="${VERSION_NAME:-dev}"

mkdir -p "$ROOT_DIR/dist"
APK_SRC="$SCRIPT_DIR/app/build/outputs/apk/release/app-release.apk"
APK_DST="$ROOT_DIR/dist/PhoneAiAudit-v${VERSION_NAME}.apk"
cp -f "$APK_SRC" "$APK_DST"
cp -f "$APK_SRC" "$ROOT_DIR/dist/PhoneAiAudit-latest.apk"
ls -lh "$APK_DST"
