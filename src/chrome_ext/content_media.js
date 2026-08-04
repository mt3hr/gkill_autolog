// 動画・音楽の実再生時間を測って Service Worker へ報告する。
//
// 対象は YouTube / YouTube Music に限らない。ページの <video> / <audio> が
// 実際に再生された秒数を数える。
//   - 一時停止している間は数えない
//   - 広告の再生時間は数えない (YouTube 系 = YouTube / YouTube Music でのみ判定できる)
//   - シークで飛んだ分は数えない
//   - ループ再生・短い素材は数えない (装飾目的の自動再生を除くため)
//   - YouTube 系以外では消音の再生も数えない (音を消した自動再生は視聴ではない)
//
// 数えるコンテンツが変わったらそこで区切る。自動再生で次の動画・次の曲へ
// 進んだ場合も1本ずつ別の再生として報告する（要件 §8.1）。
// タイトルは報告時ではなく計測中に取り込む。切り替わりに気づくのは切り替わった
// 後なので、報告時に読むと次のコンテンツのタイトルが載ってしまう。
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

  // THUMBNAIL_VIDEO_ID はサムネイルURLに埋まっている動画ID。
  // https://i.ytimg.com/vi/<動画ID>/... と /vi_webp/<動画ID>/... の両方を受ける。
  // 動画IDは11文字の URL-safe base64。
  const THUMBNAIL_VIDEO_ID = /\/vi(?:_[a-z]+)?\/([A-Za-z0-9_-]{11})\//;

  const host = location.hostname;
  const isYouTube = host === "www.youtube.com" || host === "m.youtube.com" || host === "youtube.com";
  const isYouTubeMusic = host === "music.youtube.com";
  const isYouTubeFamily = isYouTube || isYouTubeMusic;

  // service は再生元の種別。YouTube 系以外はすべて web。
  const service = isYouTubeMusic ? "youtube_music" : isYouTube ? "youtube" : "web";

  // いま計測している再生。コンテンツが変わるたびに作り直す。
  let play = null;
  let lastTickAt = Date.now();

  // MEDIA_LOST_GRACE_TICKS は計測対象が見えなくなってから確定を待つ tick 数。
  //
  // SPA がプレイヤーを差し替える瞬間、<video> が一時的に DOM から消えたり
  // 長さ不明になったりする。即座に確定すると1本の視聴が分断される。
  const MEDIA_LOST_GRACE_TICKS = 3;
  let mediaLostTicks = 0;

  // youtubeVideoId は URL から動画IDを取り出す。取れなければ null。
  // Shorts は `?v=` を持たず、パスに動画IDが入っている。
  function youtubeVideoId() {
    if (!isYouTubeFamily) {
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

  // metadataVideoId は MediaSession のサムネイルから動画IDを取り出す。
  // 取れなければ null。
  //
  // URL に `?v=` が付かない場面でも動画IDを確認できる。YouTube Music で
  // ライブラリやホームを見ながら聴いている間と、YouTube 本体のミニプレイヤーが
  // これに当たる。サムネイルURLのパスに入っているのは動画IDそのものなので、
  // ここから正式URLを組み立てるのは推測ではない（要件 §8.2）。
  //
  // 取り出しは YouTube 系だけで行う。他のサイトのアートワークURLがたまたま
  // 同じ形をしていても、それが YouTube の動画IDである保証がないため。
  function metadataVideoId() {
    if (!isYouTubeFamily) {
      return null;
    }
    const metadata = navigator.mediaSession && navigator.mediaSession.metadata;
    if (!metadata || !metadata.artwork) {
      return null;
    }
    for (const artwork of metadata.artwork) {
      const matched = THUMBNAIL_VIDEO_ID.exec(artwork.src || "");
      if (matched) {
        return matched[1];
      }
    }
    return null;
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

  // contentURL は再生していたコンテンツのURLを返す。確定できなければ空文字。
  //
  // YouTube 系で動画IDが取れたときだけ、プレイリストなどのパラメータを落とした
  // 正式URLを組み立てる（要件 §8.1）。それ以外は開いていたURLをそのまま使う。
  //
  // YouTube 系で動画IDを取れなかった再生は URL 無しにする。開いていたページは
  // ライブラリやホームで、再生していたコンテンツのURLではないため。
  // 検索URLや推測したURLを作ってはならない（要件 §8.2）。
  function contentURL(videoId) {
    if (videoId) {
      const watchHost = service === "youtube_music" ? "music.youtube.com" : "www.youtube.com";
      return `https://${watchHost}/watch?v=${videoId}`;
    }
    if (isYouTubeFamily) {
      return "";
    }
    return pageURL();
  }

  // contentKey は「同じ再生か」を判定する鍵。変わったら別の再生として数え直す。
  // 数える対象でなければ null を返す。
  //
  // YouTube 系は動画ID。取れないときは YouTube Music に限り、MediaSession の
  // 曲名を鍵にする。アルバムアートが googleusercontent.com だと動画IDを取れないが、
  // 聴いていた事実は残せるため（URL 無しの TimeIs になる。要件 §8.1）。
  //
  // YouTube 本体で動画IDを取れないのはホーム・検索結果で、サムネイルの
  // プレビューが鳴っているだけのことがある。見ていない動画を残さないよう数えない。
  //
  // YouTube 系以外はページURL。
  function contentKey(videoId) {
    if (videoId) {
      return videoId;
    }
    if (isYouTubeMusic) {
      const metadata = sessionMetadata();
      return metadata ? `title:${metadata.title} / ${metadata.artist}` : null;
    }
    if (isYouTubeFamily) {
      return null;
    }
    return pageURL();
  }

  // isAdShowing は広告を再生中かを返す。YouTube でのみ判定できる。
  function isAdShowing() {
    if (!isYouTubeFamily) {
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
    if (media.muted && !isYouTubeFamily) {
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

  // sessionMetadata は MediaSession が持つ正式なタイトルとアーティストを返す。
  // 持っていなければ null。ページタイトルで代用はしない。
  function sessionMetadata() {
    const metadata = navigator.mediaSession && navigator.mediaSession.metadata;
    if (!metadata || !metadata.title) {
      return null;
    }
    return { title: metadata.title, artist: metadata.artist || "" };
  }

  // mediaMetadata は MediaSession が持つ正式なタイトルとアーティストを返す。
  // 無ければページタイトルを使う。それ以上の推測はしない。
  function mediaMetadata() {
    return sessionMetadata() || { title: document.title || "", artist: "" };
  }

  // adoptMetadata はいま計測している再生のタイトルを取り込む。
  //
  // タイトルを送信時に読んではならない。自動再生で次の動画へ移ったことに
  // 気づくのは切り替わりの後で、そのとき DOM のタイトルは既に次の動画のものに
  // なっている。そこで前の再生の最終報告を出すため、送信時に読むと
  // 「1つ後ろの動画のタイトル」が前の再生に載ってしまう。
  //
  // 計測中に取り込んでおけば、最終報告もそのときの値をそのまま送れる。
  function adoptMetadata() {
    if (!play) {
      return;
    }
    // YouTube 系はメタデータが誰のものか確認できる。切り替わりの前後で
    // URL とメタデータがずれている間は、追いつくまで前の値を保つ。
    const metaVideoId = metadataVideoId();
    if (play.videoId && metaVideoId && metaVideoId !== play.videoId) {
      return;
    }
    const { title, artist } = mediaMetadata();
    if (!title) {
      return;
    }
    play.title = title;
    play.artist = artist;
  }

  // newPlayId は再生1回ぶんの識別子を作る。
  //
  // crypto.randomUUID は secure context 限定で、平文 http のページ
  // (宅内サーバなど) では存在しない。ここで毎秒例外を投げると
  // そのページの再生が一切記録されなくなるため、代わりの乱数で組み立てる。
  function newPlayId() {
    if (crypto.randomUUID) {
      return crypto.randomUUID();
    }
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  function startPlay(key, url, videoId, media) {
    play = {
      playId: newPlayId(),
      key,
      url,
      videoId: videoId || "",
      service,
      // タイトルは adoptMetadata がこの直後に入れる。
      title: "",
      artist: "",
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
    try {
      chrome.runtime.sendMessage({
        type: "media_progress",
        playId: play.playId,
        service: play.service,
        url: play.url,
        videoId: play.videoId,
        // 計測中に取り込んだ「この再生の」タイトル。いま DOM にある値ではない。
        title: play.title,
        artist: play.artist,
        playedSeconds: Math.round(play.playedSeconds * 10) / 10,
        startedAt: play.startedAt,
        endedAt: play.endedAt,
        finished,
        // 応答が返らないことがある (Service Worker の入れ替わり時など)。
        // 次の報告で送り直すので、拒否は握り潰してよい。
      }).catch(() => {});
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
      if (!play) {
        // 再生の無いページで毎秒 DOM を走査し続けない。
        // 次に再生が始まれば play イベントで動き出す。
        stopTicker();
        return;
      }
      // 計測対象が見えなくなっても、すぐには確定しない。
      //
      // YouTube は広告を同じ <video> 要素で再生し、その間は要素の duration が
      // 広告の長さになる。30秒未満の広告 (スキップ可能広告の大半) では
      // isCountable が要素ごと落とすため「対象なし」に見えるが、
      // ここで確定すると1本の視聴が広告のたびに分断され、
      // 同じ動画の URLog が広告の数だけできてしまう。
      // 広告の表示中は何 tick でも待つ。広告の再生時間はもともと数えない。
      if (isAdShowing()) {
        mediaLostTicks = 0;
        return;
      }
      // 広告でもないのに消えた。SPA の差し替えの瞬間かもしれないので
      // 数 tick だけ様子を見て、それでも戻らなければ確定する。
      // 一覧やホームへ戻った場合もここを通り、数秒遅れて確定する。
      mediaLostTicks++;
      if (mediaLostTicks < MEDIA_LOST_GRACE_TICKS) {
        return;
      }
      report(true);
      play = null;
      mediaLostTicks = 0;
      stopTicker();
      return;
    }
    mediaLostTicks = 0;

    // 動画IDは URL から取り、取れなければ MediaSession のサムネイルから取る。
    // ミニプレイヤーや YouTube Music のライブラリ表示中は URL に `?v=` が付かない。
    const videoId = youtubeVideoId() || metadataVideoId();
    const key = contentKey(videoId);

    if (!key) {
      // 何を再生しているのか確認できない。YouTube 本体のホーム・検索結果で
      // サムネイルのプレビューが鳴っているだけのことがある（要件 §8.1）。
      // ページのURLで記録すると、見ていない動画の再生まで残ってしまう。
      if (play) {
        report(true);
        play = null;
      }
      return;
    }

    if (!play || play.key !== key) {
      // 別のコンテンツへ切り替わった。自動再生で次へ進んだ場合もここ。
      // 前の再生を確定させてから新しく数え始める。1回の再生につき1件になる。
      if (play) {
        report(true);
      }
      startPlay(key, contentURL(videoId), videoId, media);
    }

    // タイトルはここで取り込む。前の再生の最終報告より後なので、
    // 次の動画のタイトルが前の再生に載ることはない。
    adoptMetadata();

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
  //
  // 報告のあとは play を捨てる。bfcache から復元されて再生が続くことがあり、
  // 捨てないと累計の playedSeconds を持ったまま同じ playId の報告が再開され、
  // 確定済みの分を含む2本目のイベントができてしまう。
  // 復元後の再生は tick が新しい playId で数え直す。
  addEventListener("pagehide", () => {
    report(true);
    play = null;
  });
  addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      report(false);
    }
  });
})();
