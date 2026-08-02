// 動画・音楽の実再生時間を測って Service Worker へ報告する。
//
// 対象は YouTube / YouTube Music に限らない。ページの <video> / <audio> が
// 実際に再生された秒数を数える。
//   - 一時停止している間は数えない
//   - 広告の再生時間は数えない (YouTube のみ判定できる)
//   - シークで飛んだ分は数えない
//   - ループ再生・短い素材は数えない (装飾目的の自動再生を除くため)
//
// URL は実際に開いていたページのものだけを載せる。
// 検索URLや推測したURLを作ってはならない（要件 §8.2）。

(() => {
  // TICK_MS は再生時間を積み上げる間隔。
  const TICK_MS = 1000;

  // REPORT_MS は Service Worker へ途中経過を送る間隔。
  // タブが突然閉じられても、ここまでの再生実績は残る。
  const REPORT_MS = 10000;

  // MIN_MEDIA_DURATION_SECONDS はこれ未満の長さの素材を数えない。
  // 背景で流れる短い映像やループ素材を除くため。
  // 長さが分からない (ライブ配信など) 場合は対象にする。
  const MIN_MEDIA_DURATION_SECONDS = 30;

  const host = location.hostname;
  const isYouTube = host === "www.youtube.com" || host === "m.youtube.com" || host === "youtube.com";
  const isYouTubeMusic = host === "music.youtube.com";

  // service は再生元の種別。YouTube 系以外はすべて web。
  const service = isYouTubeMusic ? "youtube_music" : isYouTube ? "youtube" : "web";

  // いま計測している再生。コンテンツが変わるたびに作り直す。
  let play = null;
  let lastTickAt = Date.now();

  // youtubeVideoId は URL から動画IDを取り出す。取れなければ null。
  // Shorts は `?v=` を持たず、パスに動画IDが入っている。
  function youtubeVideoId() {
    if (!isYouTube && !isYouTubeMusic) {
      return null;
    }
    try {
      const url = new URL(location.href);
      const query = url.searchParams.get("v");
      if (query) {
        return query;
      }
      const shorts = url.pathname.match(/^\/shorts\/([A-Za-z0-9_-]{11})/);
      return shorts ? shorts[1] : null;
    } catch {
      return null;
    }
  }

  // pageURL はハッシュを除いた現在のURL。
  // ハッシュはページ内の位置なので、同じコンテンツの再生を分けてしまう。
  function pageURL() {
    try {
      const url = new URL(location.href);
      url.hash = "";
      return url.href;
    } catch {
      return location.href;
    }
  }

  // contentURL は再生していたコンテンツのURLを返す。
  //
  // YouTube 系で動画IDが取れたときだけ、プレイリストなどのパラメータを落とした
  // 正式URLを組み立てる（要件 §8.1）。それ以外は開いていたURLをそのまま使う。
  function contentURL(videoId) {
    if (videoId) {
      const watchHost = service === "youtube_music" ? "music.youtube.com" : "www.youtube.com";
      return `https://${watchHost}/watch?v=${videoId}`;
    }
    return pageURL();
  }

  // contentKey は「同じ再生か」を判定する鍵。
  // YouTube 系は動画ID、それ以外はページURL。変わったら別の再生として数え直す。
  function contentKey(videoId) {
    return videoId || pageURL();
  }

  // isAdShowing は広告を再生中かを返す。YouTube でのみ判定できる。
  function isAdShowing() {
    if (!isYouTube && !isYouTubeMusic) {
      return false;
    }
    return document.querySelector(".ad-showing, .ad-interrupting") !== null;
  }

  // isCountable は再生時間を数えてよい要素かを返す。
  //
  // ループ再生と短すぎる素材は、背景で流す装飾目的の映像とみなして数えない。
  // 長さが分からないもの (ライブ配信など) は対象にする。
  //
  // 消音は YouTube 系だけ数える。一般のサイトでは消音の自動再生が装飾の目印だが、
  // YouTube で音を切るのは利用者が意図してやったことで、視聴していないとは言えない。
  function isCountable(media) {
    if (media.loop) {
      return false;
    }
    if (media.muted && !isYouTube && !isYouTubeMusic) {
      return false;
    }
    if (Number.isFinite(media.duration) && media.duration < MIN_MEDIA_DURATION_SECONDS) {
      return false;
    }
    return true;
  }

  // findMedia は数える対象の要素を返す。
  // 再生中のものを優先し、無ければ計測中の再生を続けられるよう最初の要素を返す。
  function findMedia() {
    const all = Array.from(document.querySelectorAll("video, audio")).filter(isCountable);
    return all.find((m) => !m.paused && !m.ended) || all[0] || null;
  }

  // mediaMetadata は MediaSession が持つ正式なタイトルとアーティストを返す。
  // 無ければページタイトルを使う。それ以上の推測はしない。
  function mediaMetadata() {
    const metadata = navigator.mediaSession && navigator.mediaSession.metadata;
    if (metadata && metadata.title) {
      return { title: metadata.title, artist: metadata.artist || "" };
    }
    return { title: document.title || "", artist: "" };
  }

  function startPlay(key, url, media) {
    play = {
      playId: crypto.randomUUID(),
      key,
      url,
      videoId: youtubeVideoId() || "",
      service,
      playedSeconds: 0,
      startedAt: Date.now(),
      endedAt: Date.now(),
      lastCurrentTime: media ? media.currentTime : 0,
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

    const media = findMedia();

    if (!media) {
      // 数える対象が無くなった。一覧やホームへ戻った場合もここ。
      if (play) {
        report(true);
        play = null;
      }
      // 再生の無いページで毎秒 DOM を走査し続けない。
      // 次に再生が始まれば play イベントで動き出す。
      stopTicker();
      return;
    }

    const videoId = youtubeVideoId();

    if ((isYouTube || isYouTubeMusic) && !videoId) {
      // 一覧・ホーム・検索結果。個別コンテンツを開いていない（要件 §8.1）。
      // ページのURLで記録すると、見ていない動画のプレビュー再生まで残ってしまう。
      if (play) {
        report(true);
        play = null;
      }
      return;
    }

    const key = contentKey(videoId);

    if (!play || play.key !== key) {
      // 別のコンテンツへ切り替わった。前の再生を確定させてから新しく数え始める。
      if (play) {
        report(true);
      }
      startPlay(key, contentURL(videoId), media);
    }

    if (media.paused || media.ended || isAdShowing()) {
      // 一時停止中と広告中は数えない。再開時に飛びとみなさないよう位置だけ追う。
      play.lastCurrentTime = media.currentTime;
    } else {
      const delta = media.currentTime - play.lastCurrentTime;
      play.lastCurrentTime = media.currentTime;

      // シークで飛んだ分を除く。
      // バックグラウンドタブではタイマーが間引かれるため、
      // 経過した実時間（再生速度を考慮）までは正当な再生として認める。
      const maxDelta = wallDelta * (media.playbackRate || 1) + 1;
      if (delta > 0 && delta <= maxDelta) {
        play.playedSeconds += delta;
        play.endedAt = now;
      }
    }

    if (now - play.lastReportAt >= REPORT_MS) {
      report(false);
    }
  }

  // 計測は再生が始まってから動かす。
  // この拡張はすべてのサイトに入るので、再生の無いページで毎秒 DOM を
  // 走査し続けないようにする。
  let ticker = null;

  function startTicker() {
    if (ticker) {
      return;
    }
    lastTickAt = Date.now();
    ticker = setInterval(tick, TICK_MS);
  }

  function stopTicker() {
    if (!ticker) {
      return;
    }
    clearInterval(ticker);
    ticker = null;
  }

  // play / playing は要素で起きて上へ伝わらないので、捕捉フェーズで受ける。
  addEventListener("play", startTicker, true);
  addEventListener("playing", startTicker, true);

  // 拡張を読み込み直した直後など、すでに再生が始まっていることがある。
  if (findMedia()) {
    startTicker();
  }

  // タブを閉じる・別ページへ移るときは最後の報告を試みる。
  // 届かなくても、直前の途中経過が Service Worker 側に残っている。
  addEventListener("pagehide", () => report(true));
  addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      report(false);
    }
  });
})();
