// YouTube / YouTube Music の実再生時間を測って Service Worker へ報告する。
//
// 測るのは「実際に再生された秒数」だけ。
//   - 一時停止している間は数えない
//   - 広告の再生時間は数えない
//   - シークで飛んだ分は数えない
//
// URL や動画IDを確定できない場合は何も報告しない。
// 検索URLや推測したURLを作ってはならない（要件 §8.2）。

(() => {
  // TICK_MS は再生時間を積み上げる間隔。
  const TICK_MS = 1000;

  // REPORT_MS は Service Worker へ途中経過を送る間隔。
  // タブが突然閉じられても、ここまでの再生実績は残る。
  const REPORT_MS = 10000;

  const service = location.hostname === "music.youtube.com" ? "youtube_music" : "youtube";

  // いま計測している再生。動画が変わるたびに作り直す。
  let play = null;
  let lastTickAt = Date.now();

  // currentVideoId は URL から動画IDを取り出す。取れなければ null。
  function currentVideoId() {
    try {
      return new URL(location.href).searchParams.get("v");
    } catch {
      return null;
    }
  }

  // canonicalURL は個別コンテンツの正式URLを組み立てる。
  // プレイリストやアルバムのパラメータは含めない（要件 §8.1）。
  function canonicalURL(videoId) {
    const host = service === "youtube_music" ? "music.youtube.com" : "www.youtube.com";
    return `https://${host}/watch?v=${videoId}`;
  }

  // isAdShowing は広告を再生中かを返す。
  function isAdShowing() {
    return document.querySelector(".ad-showing, .ad-interrupting") !== null;
  }

  function findVideo() {
    // 複数ある場合は再生位置が進んでいるものを選ぶ。
    const videos = Array.from(document.querySelectorAll("video"));
    return videos.find((v) => !v.paused && !v.ended) || videos[0] || null;
  }

  // mediaTitle は MediaSession が持つ正式なタイトルとアーティストを返す。
  // 取得できなければ空のままにする。ページタイトルからの推測はしない。
  function mediaMetadata() {
    const metadata = navigator.mediaSession && navigator.mediaSession.metadata;
    if (!metadata) {
      return { title: "", artist: "" };
    }
    return { title: metadata.title || "", artist: metadata.artist || "" };
  }

  function startPlay(videoId, video) {
    play = {
      playId: crypto.randomUUID(),
      videoId,
      url: canonicalURL(videoId),
      service,
      playedSeconds: 0,
      startedAt: Date.now(),
      endedAt: Date.now(),
      lastCurrentTime: video ? video.currentTime : 0,
      lastReportAt: 0,
    };
  }

  function report(finished) {
    if (!play || play.playedSeconds <= 0) {
      return;
    }
    const { title, artist } = mediaMetadata();
    try {
      chrome.runtime.sendMessage({
        type: "media_progress",
        playId: play.playId,
        service: play.service,
        url: play.url,
        videoId: play.videoId,
        title,
        artist,
        playedSeconds: Math.round(play.playedSeconds * 10) / 10,
        startedAt: play.startedAt,
        endedAt: play.endedAt,
        finished,
      });
    } catch {
      // 拡張が再読み込みされた直後などは送れない。次の報告で送り直す。
    }
    play.lastReportAt = Date.now();
  }

  function tick() {
    const now = Date.now();
    const wallDelta = (now - lastTickAt) / 1000;
    lastTickAt = now;

    const videoId = currentVideoId();
    const video = findVideo();

    if (!videoId) {
      // 一覧やホームなど、個別コンテンツを開いていない状態。
      if (play) {
        report(true);
        play = null;
      }
      return;
    }

    if (!play || play.videoId !== videoId) {
      // 別の動画へ切り替わった。前の再生を確定させてから新しく数え始める。
      if (play) {
        report(true);
      }
      startPlay(videoId, video);
    }

    if (!video) {
      return;
    }

    if (video.paused || video.ended || isAdShowing()) {
      // 一時停止中と広告中は数えない。再開時に飛びとみなさないよう位置だけ追う。
      play.lastCurrentTime = video.currentTime;
    } else {
      const delta = video.currentTime - play.lastCurrentTime;
      play.lastCurrentTime = video.currentTime;

      // シークで飛んだ分を除く。
      // バックグラウンドタブではタイマーが間引かれるため、
      // 経過した実時間（再生速度を考慮）までは正当な再生として認める。
      const maxDelta = wallDelta * (video.playbackRate || 1) + 1;
      if (delta > 0 && delta <= maxDelta) {
        play.playedSeconds += delta;
        play.endedAt = now;
      }
    }

    if (now - play.lastReportAt >= REPORT_MS) {
      report(false);
    }
  }

  setInterval(tick, TICK_MS);

  // タブを閉じる・別ページへ移るときは最後の報告を試みる。
  // 届かなくても、直前の途中経過が Service Worker 側に残っている。
  addEventListener("pagehide", () => report(true));
  addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      report(false);
    }
  });
})();
