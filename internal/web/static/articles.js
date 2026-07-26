"use strict";

const articleListState = {
  grid: null,
  requestSequence: 0,
};

const articleDetailState = {
  articleID: "",
  hasMore: false,
  loading: false,
  nextBodyOffset: 0,
};

const articleDateFormatter = new Intl.DateTimeFormat("ja-JP", {
  dateStyle: "medium",
  timeStyle: "short",
});

/**
 * 値を画面表示用の文字列へ変換します。
 *
 * 機能:
 *   - null、undefined、空文字を指定された代替文字列へ置き換える
 *   - API由来の値を安全にtextContentへ設定できる文字列へ揃える
 *
 * @param {unknown} value - 変換するAPI応答値。
 * @param {string} [fallback="—"] - 値が空の場合に表示する文字列。
 * @returns {string} 画面表示用の文字列。
 */
function toDisplayString(value, fallback = "—") {
  if (value === null || value === undefined) {
    return fallback;
  }

  const text = String(value).trim();
  return text === "" ? fallback : text;
}

// ----------------------------------------

/**
 * APIの日時文字列を日本語のローカル日時へ整形します。
 *
 * 機能:
 *   - RFC3339日時をブラウザーのローカルタイムゾーンで表示する
 *   - 空または解析できない日時をダッシュ表示にする
 *
 * @param {unknown} value - APIから返された日時。
 * @returns {string} 整形済み日時、または値がないことを示すダッシュ。
 */
function formatArticleDate(value) {
  if (typeof value !== "string" || value.trim() === "") {
    return "—";
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return articleDateFormatter.format(date);
}

// ----------------------------------------

/**
 * datetime-localの値をRFC3339形式へ変換します。
 *
 * 機能:
 *   - ユーザーが入力したローカル日時を絶対日時へ変換する
 *   - 空欄は検索条件を省略するため空文字のまま返す
 *
 * @param {string} value - datetime-local入力の値。
 * @returns {string} RFC3339形式の日時、または空文字。
 * @throws {Error} 入力日時を解析できない場合にスローします。
 */
function toRFC3339(value) {
  const trimmedValue = value.trim();
  if (trimmedValue === "") {
    return "";
  }

  const date = new Date(trimmedValue);
  if (Number.isNaN(date.getTime())) {
    throw new Error("日時条件を正しく入力してください。");
  }
  return date.toISOString();
}

// ----------------------------------------

/**
 * 5個の検索語入力から重複のない検索語を取得します。
 *
 * 機能:
 *   - #article-keyword-1 から #article-keyword-5 を入力順に読み取る
 *   - 空欄を除外し、大文字小文字違いを含む重複語を取り除く
 *   - 1語あたり200文字を超える入力を拒否する
 *
 * @param {HTMLFormElement} form - 記事検索フォーム。
 * @returns {string[]} AND検索へ渡す最大5個の検索語。
 * @throws {Error} 検索語入力が見つからない、または長すぎる場合にスローします。
 */
function collectArticleKeywords(form) {
  const keywords = [];
  const seen = new Set();

  for (let index = 1; index <= 5; index += 1) {
    const input = form.querySelector(`#article-keyword-${index}`);
    if (!(input instanceof HTMLInputElement)) {
      throw new Error("検索画面を初期化できませんでした。");
    }

    const keyword = input.value.trim();
    if (Array.from(keyword).length > 200) {
      throw new Error("検索語は1語あたり200文字以内にしてください。");
    }
    const normalizedKeyword = keyword.toLocaleLowerCase();
    if (keyword !== "" && !seen.has(normalizedKeyword)) {
      seen.add(normalizedKeyword);
      keywords.push(keyword);
    }
  }
  return keywords;
}

// ----------------------------------------

/**
 * 記事検索フォームからAPIクエリを生成します。
 *
 * 機能:
 *   - 指定された条件だけをURLSearchParamsへ追加する
 *   - 5個の検索語入力を繰り返しkeywordsパラメーターにする
 *   - 2つの日時が逆順でも、早い日時を下限、遅い日時を上限として送信する
 *   - 最大件数をブラウザー側でも検証する
 *
 * @param {HTMLFormElement} form - 記事検索フォーム。
 * @returns {URLSearchParams} 記事一覧APIへ渡す検索条件。
 * @throws {Error} 入力条件が不正な場合にスローします。
 */
function buildArticleSearchParams(form) {
  if (!form.reportValidity()) {
    throw new Error("検索条件の入力内容を確認してください。");
  }

  const source = form.querySelector("#article-source");
  const dateFrom = form.querySelector("#article-date-from");
  const dateTo = form.querySelector("#article-date-to");
  const limit = form.querySelector("#article-limit");
  if (
    !(source instanceof HTMLSelectElement) ||
    !(dateFrom instanceof HTMLInputElement) ||
    !(dateTo instanceof HTMLInputElement) ||
    !(limit instanceof HTMLInputElement)
  ) {
    throw new Error("検索画面を初期化できませんでした。");
  }

  const keywords = collectArticleKeywords(form);

  let normalizedFromValue = toRFC3339(dateFrom.value);
  let normalizedToValue = toRFC3339(dateTo.value);
  if (
    normalizedFromValue !== "" &&
    normalizedToValue !== "" &&
    new Date(normalizedFromValue) > new Date(normalizedToValue)
  ) {
    [normalizedFromValue, normalizedToValue] = [
      normalizedToValue,
      normalizedFromValue,
    ];
  }

  const params = new URLSearchParams();
  if (source.value !== "") {
    params.set("source_id", source.value);
  }
  if (normalizedFromValue !== "") {
    params.set("date_from", normalizedFromValue);
  }
  if (normalizedToValue !== "") {
    params.set("date_to", normalizedToValue);
  }
  keywords.forEach((keyword) => params.append("keywords", keyword));
  if (limit.value.trim() !== "") {
    params.set("limit", limit.value.trim());
  }
  return params;
}

// ----------------------------------------

/**
 * Axiosエラーから利用者向けの短いメッセージを取り出します。
 *
 * 機能:
 *   - APIのerrorまたはmessage文字列を優先して表示する
 *   - 予期しない応答やネットワークエラーを固定メッセージへ置き換える
 *   - 表示文字数を制限し、textContentで扱う文字列だけを返す
 *
 * @param {unknown} error - Axiosまたは画面検証から発生したエラー。
 * @param {string} fallback - 詳細を取得できない場合の固定メッセージ。
 * @returns {string} 利用者へ表示する安全なエラーメッセージ。
 */
function getArticleAPIErrorMessage(error, fallback) {
  if (typeof error === "object" && error !== null) {
    const response = Reflect.get(error, "response");
    if (typeof response === "object" && response !== null) {
      const data = Reflect.get(response, "data");
      if (typeof data === "object" && data !== null) {
        const errorValue = Reflect.get(data, "error");
        const messageValue = Reflect.get(data, "message");
        if (typeof errorValue === "string" && errorValue.trim() !== "") {
          return errorValue.slice(0, 500);
        }
        if (typeof messageValue === "string" && messageValue.trim() !== "") {
          return messageValue.slice(0, 500);
        }
      }
    }
  }
  if (error instanceof Error && error.message !== "") {
    return error.message.slice(0, 500);
  }
  return fallback;
}

// ----------------------------------------

/**
 * 記事一覧画面の入力エラー表示を切り替えます。
 *
 * 機能:
 *   - エラー文字列をtextContentで設定する
 *   - 空文字の場合はエラー領域を非表示にする
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @param {string} message - 表示する入力エラー。空文字は非表示。
 * @returns {void} 返り値はありません。
 */
function setArticleFilterError(root, message) {
  const errorElement = root.querySelector("#article-filter-error");
  if (!(errorElement instanceof HTMLElement)) {
    return;
  }
  errorElement.textContent = message;
  errorElement.hidden = message === "";
}

// ----------------------------------------

/**
 * 記事一覧APIのエラー表示を切り替えます。
 *
 * 機能:
 *   - APIエラーをtextContentで通知領域へ設定する
 *   - 空文字の場合は通知領域を非表示にする
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @param {string} message - 表示するAPIエラー。空文字は非表示。
 * @returns {void} 返り値はありません。
 */
function setArticleListError(root, message) {
  const errorBox = root.querySelector("#article-error");
  const errorMessage = root.querySelector("#article-error-message");
  if (!(errorBox instanceof HTMLElement) || !(errorMessage instanceof HTMLElement)) {
    return;
  }
  errorMessage.textContent = message === "" ? "" : ` ${message}`;
  errorBox.hidden = message === "";
}

// ----------------------------------------

/**
 * 記事一覧の読み込み中に検索操作の可否を切り替えます。
 *
 * 機能:
 *   - 検索ボタンと条件クリアボタンの重複操作を抑止する
 *   - 検索ボタンの進行中スタイルだけを切り替え、常設の文言は表示しない
 *   - Grid.jsの表示領域へ支援技術向けの読み込み状態を設定する
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @param {boolean} loading - 読み込み中の場合はtrue。
 * @returns {void} 返り値はありません。
 */
function setArticleListLoading(root, loading) {
  const searchButton = root.querySelector("#article-search");
  const clearButton = root.querySelector("#article-clear");
  const gridContainer = root.querySelector("#article-grid");
  if (searchButton instanceof HTMLButtonElement) {
    searchButton.disabled = loading;
    searchButton.classList.toggle("is-loading", loading);
  }
  if (clearButton instanceof HTMLButtonElement) {
    clearButton.disabled = loading;
  }
  if (gridContainer instanceof HTMLElement) {
    gridContainer.setAttribute("aria-busy", String(loading));
  }
}

// ----------------------------------------

/**
 * 記事概要1件からGrid.js用の安全な見出し要素を生成します。
 *
 * 機能:
 *   - API由来の見出しをGrid.jsの仮想DOMでテキストとして表示する
 *   - 検証済みの正整数記事IDだけを詳細画面の内部リンクへ組み込む
 *   - 不正な記事IDの場合はリンクを作らず見出しだけを表示する
 *
 * @param {Record<string, unknown>} article - 記事一覧APIの記事概要。
 * @returns {unknown} Grid.jsが描画する安全な仮想DOM要素。
 */
function createArticleHeadlineCell(article) {
  const headline = toDisplayString(article.headline, "（見出しなし）");
  const articleID = Number(article.id);
  if (Number.isSafeInteger(articleID) && articleID > 0) {
    return window.gridjs.h(
      "a",
      {
        className: "headline-link",
        href: `/admin/articles/${articleID}`,
      },
      headline,
    );
  }
  return window.gridjs.h("span", {}, headline);
}

// ----------------------------------------

/**
 * 記事一覧へ表示する更新日時を選択します。
 *
 * 機能:
 *   - 取得元の更新日時が設定されている場合はupdated_atを使用する
 *   - 更新日時がない場合は投稿日時であるpublished_atへフォールバックする
 *   - どちらもない場合はformatArticleDateがダッシュ表示できる値を返す
 *
 * @param {Record<string, unknown>} article - 記事一覧APIの記事概要。
 * @returns {unknown} 更新日時、投稿日時、または未設定値。
 */
function getArticleListDate(article) {
  const updatedAt = article.updated_at;
  if (typeof updatedAt === "string" && updatedAt.trim() !== "") {
    return updatedAt;
  }
  return article.published_at;
}

// ----------------------------------------

/**
 * 記事概要1件を1列表示用の安全な要素へ変換します。
 *
 * 機能:
 *   - ヘッドライン、取得対象、更新日時または投稿日時を1つのセルへまとめる
 *   - API由来の文字列をGrid.jsの仮想DOMのテキスト子として描画する
 *   - 固定列幅を設けず、画面幅に応じて補足情報を折り返す
 *
 * @param {Record<string, unknown>} article - 記事一覧APIの記事概要。
 * @returns {unknown} Grid.jsが1列へ描画する安全な仮想DOM要素。
 */
function createArticleSummaryCell(article) {
  const sourceName = toDisplayString(article.source_name, "設定名なし");
  const displayDate = formatArticleDate(getArticleListDate(article));

  return window.gridjs.h(
    "div",
    { className: "article-grid-item" },
    window.gridjs.h(
      "div",
      { className: "article-grid-headline" },
      createArticleHeadlineCell(article),
    ),
    window.gridjs.h(
      "div",
      { className: "article-grid-meta" },
      window.gridjs.h("span", {}, `取得対象: ${sourceName}`),
      window.gridjs.h("span", {}, `更新／投稿日時: ${displayDate}`),
    ),
  );
}

// ----------------------------------------

/**
 * 記事概要配列をGrid.jsで一覧表示します。
 *
 * 機能:
 *   - APIが返した順序を変更せず、見出し、取得対象、更新日時を1列へまとめて表示する
 *   - 更新日時がない記事は投稿日時を表示する
 *   - 最大取得件数内をGrid.jsのローカルページングで1ページ10件に分ける
 *   - 再検索時は既存Gridのデータだけを更新して検索結果全体を置き換える
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @param {Record<string, unknown>[]} articles - APIから取得した記事概要の配列。
 * @returns {void} 返り値はありません。
 */
function renderArticleGrid(root, articles) {
  const gridContainer = root.querySelector("#article-grid");
  if (!(gridContainer instanceof HTMLElement)) {
    throw new Error("記事一覧の表示領域を初期化できませんでした。");
  }

  const gridData = articles.map((article) => [article]);
  if (articleListState.grid !== null) {
    articleListState.grid.updateConfig({ data: gridData }).forceRender();
    return;
  }

  articleListState.grid = new window.gridjs.Grid({
    columns: [
      {
        name: "記事",
        formatter: (article) => createArticleSummaryCell(article),
      },
    ],
    data: gridData,
    pagination: {
      limit: 10,
      summary: false,
    },
    search: false,
    sort: false,
    language: {
      noRecordsFound: "該当する記事はありません。",
      pagination: {
        next: "次へ",
        previous: "前へ",
      },
    },
  });
  articleListState.grid.render(gridContainer);
}

// ----------------------------------------

/**
 * 記事一覧APIを呼び出して検索結果を表示します。
 *
 * 機能:
 *   - 初回または再検索時にフォーム条件から記事を1回だけ取得する
 *   - APIの追加取得情報は使用せず、返された最大件数内をGrid.jsへ渡す
 *   - 古いリクエストの遅延応答を画面へ反映しない
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @returns {Promise<void>} 表示更新完了時に解決するPromise。
 */
async function loadArticleList(root) {
  const form = root.querySelector("#article-filter");
  if (!(form instanceof HTMLFormElement)) {
    return;
  }

  let params;
  try {
    params = buildArticleSearchParams(form);
  } catch (error) {
    setArticleFilterError(
      root,
      getArticleAPIErrorMessage(error, "検索条件を確認してください。"),
    );
    return;
  }

  setArticleFilterError(root, "");
  setArticleListError(root, "");
  const requestSequence = ++articleListState.requestSequence;
  setArticleListLoading(root, true);

  try {
    const response = await window.axios.get("/api/articles", {
      headers: { Accept: "application/json" },
      params,
    });
    if (requestSequence !== articleListState.requestSequence) {
      return;
    }

    const data = response.data;
    if (
      typeof data !== "object" ||
      data === null ||
      !Array.isArray(data.articles) ||
      data.articles.some(
        (article) => typeof article !== "object" || article === null,
      )
    ) {
      throw new Error("記事一覧APIの応答形式が正しくありません。");
    }

    renderArticleGrid(root, data.articles);
  } catch (error) {
    if (requestSequence !== articleListState.requestSequence) {
      return;
    }
    setArticleListError(
      root,
      getArticleAPIErrorMessage(error, "ネットワークまたはサーバーの状態を確認してください。"),
    );
  } finally {
    if (requestSequence === articleListState.requestSequence) {
      setArticleListLoading(root, false);
    }
  }
}

// ----------------------------------------

/**
 * 取得対象設定APIから一覧検索の選択肢を読み込みます。
 *
 * 機能:
 *   - 設定名だけを選択肢のラベルへ表示し、news_sources.idはvalueへ保持する
 *   - 無効な設定は履歴検索できるまま表示上で区別する
 *   - 取得失敗時も「すべて」での記事検索を利用可能に保つ
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @returns {Promise<void>} 選択肢の更新完了時に解決するPromise。
 */
async function loadArticleSources(root) {
  const sourceSelect = root.querySelector("#article-source");
  const sourceError = root.querySelector("#article-source-error");
  if (!(sourceSelect instanceof HTMLSelectElement) || !(sourceError instanceof HTMLElement)) {
    return;
  }

  try {
    const response = await window.axios.get("/api/sources", {
      headers: { Accept: "application/json" },
    });
    const data = response.data;
    if (typeof data !== "object" || data === null || !Array.isArray(data.sources)) {
      throw new Error("取得対象APIの応答形式が正しくありません。");
    }

    data.sources.forEach((source) => {
      if (typeof source !== "object" || source === null) {
        return;
      }
      const sourceID = Number(source.id);
      if (!Number.isSafeInteger(sourceID) || sourceID < 1) {
        return;
      }
      const option = document.createElement("option");
      option.value = String(sourceID);
      const sourceName = toDisplayString(source.name, "設定名なし");
      option.textContent = source.enabled === false ? `${sourceName}（無効）` : sourceName;
      sourceSelect.append(option);
    });
    sourceError.textContent = "";
    sourceError.hidden = true;
  } catch (error) {
    sourceError.textContent = getArticleAPIErrorMessage(
      error,
      "取得対象を読み込めませんでした。すべての記事は検索できます。",
    );
    sourceError.hidden = false;
  }
}

// ----------------------------------------

/**
 * 記事一覧画面のイベントと初回API取得を初期化します。
 *
 * 機能:
 *   - 検索と条件クリアの操作を登録する
 *   - 取得対象設定と無条件の最新記事を初期表示時に取得する
 *
 * @param {HTMLElement} root - 記事一覧画面のルート要素。
 * @returns {void} 返り値はありません。
 */
function initializeArticleList(root) {
  const form = root.querySelector("#article-filter");
  const clearButton = root.querySelector("#article-clear");
  if (
    !(form instanceof HTMLFormElement) ||
    !(clearButton instanceof HTMLButtonElement)
  ) {
    return;
  }

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    void loadArticleList(root);
  });
  clearButton.addEventListener("click", () => {
    form.reset();
    setArticleFilterError(root, "");
    void loadArticleList(root);
  });

  void loadArticleSources(root);
  void loadArticleList(root);
}

// ----------------------------------------

/**
 * 現在の管理画面URLから記事IDを取得します。
 *
 * 機能:
 *   - /admin/articles/:id の末尾にある正整数だけを受け付ける
 *   - APIパスへ任意文字列が混入しないようにIDを検証する
 *
 * @param {string} pathname - 現在のURLパス。
 * @returns {string} 検証済み記事ID。形式が不正な場合は空文字。
 */
function getArticleIDFromPath(pathname) {
  const matched = pathname.match(/^\/admin\/articles\/([1-9][0-9]*)\/?$/u);
  return matched === null ? "" : matched[1];
}

// ----------------------------------------

/**
 * canonical URLを安全なHTTPまたはHTTPS URLへ検証します。
 *
 * 機能:
 *   - 絶対URLだけをURLオブジェクトとして解析する
 *   - javascript等のHTTP/HTTPS以外のスキームを拒否する
 *
 * @param {unknown} value - APIから返されたcanonical URL。
 * @returns {URL|null} 安全なURLオブジェクト。不正または許可外の場合はnull。
 */
function parseSafeArticleURL(value) {
  if (typeof value !== "string" || value.trim() === "") {
    return null;
  }

  try {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return null;
    }
    return url;
  } catch {
    return null;
  }
}

// ----------------------------------------

/**
 * 記事詳細APIのメタデータを画面へ反映します。
 *
 * 機能:
 *   - 見出し、取得対象、日時などをtextContentで表示する
 *   - 安全性を検証したcanonical URLだけを外部リンクへ設定する
 *   - 初回チャンクの取得時だけ画面メタデータを更新する
 *
 * @param {HTMLElement} root - 記事詳細画面のルート要素。
 * @param {Record<string, unknown>} article - 記事詳細APIのarticle値。
 * @returns {void} 返り値はありません。
 */
function renderArticleDetailMetadata(root, article) {
  const headline = toDisplayString(article.headline, "（見出しなし）");
  const sourceName = toDisplayString(article.source_name, "設定名なし");
  const sourceID = Number(article.source_id);
  const sourceLabel =
    Number.isSafeInteger(sourceID) && sourceID > 0
      ? `${sourceName}（ID: ${sourceID}）`
      : sourceName;

  const textValues = new Map([
    ["#article-detail-headline", headline],
    [
      "#article-detail-eyebrow",
      `${sourceName} / ${toDisplayString(article.provider, "取得元不明")}`,
    ],
    [
      "#article-detail-dates",
      `公開 ${formatArticleDate(article.published_at)} ・ 取得 ${formatArticleDate(article.fetched_at)}`,
    ],
    ["#article-detail-source", sourceLabel],
    ["#article-detail-provider", toDisplayString(article.provider)],
    ["#article-detail-id", toDisplayString(article.id)],
    ["#article-detail-external-id", toDisplayString(article.external_id)],
    ["#article-detail-revision-id", toDisplayString(article.revision_id)],
    ["#article-detail-updated-at", formatArticleDate(article.updated_at)],
  ]);
  textValues.forEach((text, selector) => {
    const element = root.querySelector(selector);
    if (element instanceof HTMLElement) {
      element.textContent = text;
    }
  });
  document.title = `${headline} | NewsStore`;

  const canonicalField = root.querySelector("#article-detail-canonical-field");
  const canonicalLink = root.querySelector("#article-detail-canonical-url");
  if (canonicalField instanceof HTMLElement && canonicalLink instanceof HTMLAnchorElement) {
    const safeURL = parseSafeArticleURL(article.canonical_url);
    canonicalField.hidden = safeURL === null;
    canonicalLink.removeAttribute("href");
    canonicalLink.textContent = "";
    if (safeURL !== null) {
      canonicalLink.href = safeURL.href;
      canonicalLink.textContent = safeURL.href;
    }
  }
}

// ----------------------------------------

/**
 * 記事詳細画面の致命的なAPIエラーを表示します。
 *
 * 機能:
 *   - 初回記事取得に失敗した場合の通知をtextContentで設定する
 *   - 詳細コンテンツとローディング表示を非表示にする
 *
 * @param {HTMLElement} root - 記事詳細画面のルート要素。
 * @param {string} message - 利用者へ表示するエラー内容。
 * @returns {void} 返り値はありません。
 */
function showArticleDetailError(root, message) {
  const loading = root.querySelector("#article-detail-loading");
  const content = root.querySelector("#article-detail-content");
  const errorBox = root.querySelector("#article-detail-error");
  const errorMessage = root.querySelector("#article-detail-error-message");
  if (loading instanceof HTMLElement) {
    loading.hidden = true;
  }
  if (content instanceof HTMLElement) {
    content.hidden = true;
  }
  if (errorBox instanceof HTMLElement) {
    errorBox.hidden = false;
  }
  if (errorMessage instanceof HTMLElement) {
    errorMessage.textContent = ` ${message}`;
  }
}

// ----------------------------------------

/**
 * 記事本文の追加読込状態を更新します。
 *
 * 機能:
 *   - 続きがある場合だけ本文の「続きを読む」を表示する
 *   - API呼び出し中はボタンを無効化して進行状況を通知する
 *   - エラーまたは完了メッセージをtextContentで表示する
 *
 * @param {HTMLElement} root - 記事詳細画面のルート要素。
 * @param {string} status - 本文取得に関する状態メッセージ。
 * @returns {void} 返り値はありません。
 */
function updateArticleBodyLoading(root, status) {
  const area = root.querySelector("#article-body-load-more-area");
  const button = root.querySelector("#article-body-load-more");
  const statusElement = root.querySelector("#article-body-status");
  if (area instanceof HTMLElement) {
    area.hidden = !articleDetailState.hasMore;
  }
  if (button instanceof HTMLButtonElement) {
    button.disabled = articleDetailState.loading;
    button.classList.toggle("is-loading", articleDetailState.loading);
  }
  if (statusElement instanceof HTMLElement) {
    statusElement.textContent = status;
  }
}

// ----------------------------------------

/**
 * 記事詳細APIから本文チャンクを取得して表示します。
 *
 * 機能:
 *   - 初回は本文先頭16000文字とメタデータを取得する
 *   - 追加読込時はnext_body_offsetから次の16000文字を取得する
 *   - 初回の空本文表示を維持し、追加本文は安全なTextNodeとして末尾へ追記する
 *
 * @param {HTMLElement} root - 記事詳細画面のルート要素。
 * @param {boolean} append - 本文の続きを追記する場合はtrue。
 * @returns {Promise<void>} 本文表示の更新完了時に解決するPromise。
 */
async function loadArticleDetail(root, append) {
  if (articleDetailState.loading || articleDetailState.articleID === "") {
    return;
  }

  const body = root.querySelector("#article-detail-body");
  const loading = root.querySelector("#article-detail-loading");
  const content = root.querySelector("#article-detail-content");
  const errorBox = root.querySelector("#article-detail-error");
  if (
    !(body instanceof HTMLElement) ||
    !(loading instanceof HTMLElement) ||
    !(content instanceof HTMLElement) ||
    !(errorBox instanceof HTMLElement)
  ) {
    return;
  }

  articleDetailState.loading = true;
  updateArticleBodyLoading(root, append ? "本文の続きを取得しています…" : "");
  let finalStatus = "";

  const params = new URLSearchParams();
  params.set("body_offset", append ? String(articleDetailState.nextBodyOffset) : "0");
  params.set("body_limit", "16000");

  try {
    const response = await window.axios.get(
      `/api/articles/${articleDetailState.articleID}`,
      {
        headers: { Accept: "application/json" },
        params,
      },
    );
    const data = response.data;
    if (
      typeof data !== "object" ||
      data === null ||
      typeof data.article !== "object" ||
      data.article === null
    ) {
      throw new Error("記事詳細APIの応答形式が正しくありません。");
    }

    const article = data.article;
    if (!append) {
      renderArticleDetailMetadata(root, article);
      body.textContent =
        typeof article.body_text === "string" && article.body_text !== ""
          ? article.body_text
          : "本文はありません";
      loading.hidden = true;
      errorBox.hidden = true;
      content.hidden = false;
    } else {
      const bodyChunk =
        typeof article.body_text === "string" ? article.body_text : "";
      if (bodyChunk !== "") {
        body.appendChild(document.createTextNode(bodyChunk));
      }
    }

    const nextBodyOffset = Number(article.next_body_offset);
    articleDetailState.hasMore =
      article.has_more === true &&
      Number.isSafeInteger(nextBodyOffset) &&
      nextBodyOffset > articleDetailState.nextBodyOffset;
    articleDetailState.nextBodyOffset = articleDetailState.hasMore
      ? nextBodyOffset
      : articleDetailState.nextBodyOffset;
    finalStatus = articleDetailState.hasMore
      ? "本文をさらに読み込めます。"
      : append
        ? "本文をすべて表示しました。"
        : "";
  } catch (error) {
    const message = getArticleAPIErrorMessage(
      error,
      "ネットワークまたはサーバーの状態を確認してください。",
    );
    if (!append) {
      showArticleDetailError(root, message);
    } else {
      finalStatus = `本文の続きを取得できませんでした。${message}`;
    }
  } finally {
    articleDetailState.loading = false;
    updateArticleBodyLoading(root, finalStatus);
  }
}

// ----------------------------------------

/**
 * 記事詳細画面のAPI取得と本文追加操作を初期化します。
 *
 * 機能:
 *   - URLから検証済み記事IDを取得する
 *   - 初回本文チャンクを取得し、「続きを読む」の操作を登録する
 *
 * @param {HTMLElement} root - 記事詳細画面のルート要素。
 * @returns {void} 返り値はありません。
 */
function initializeArticleDetail(root) {
  articleDetailState.articleID = getArticleIDFromPath(window.location.pathname);
  if (articleDetailState.articleID === "") {
    showArticleDetailError(root, "記事IDが正しくありません。");
    return;
  }

  const loadMoreButton = root.querySelector("#article-body-load-more");
  if (loadMoreButton instanceof HTMLButtonElement) {
    loadMoreButton.addEventListener("click", () => {
      void loadArticleDetail(root, true);
    });
  }
  void loadArticleDetail(root, false);
}

// ----------------------------------------

/**
 * AxiosとGrid.jsを使用する記事一覧または詳細画面を初期化します。
 *
 * 機能:
 *   - 現在のHTMLに存在する記事画面だけを初期化する
 *   - Axiosが読み込めない場合は画面上へ固定エラーを表示する
 *   - 記事一覧でGrid.jsが読み込めない場合は画面上へ固定エラーを表示する
 *
 * @returns {void} 返り値はありません。
 */
function initializeArticlePages() {
  const listRoot = document.querySelector("#article-list-page");
  const detailRoot = document.querySelector("#article-detail-page");
  if (typeof window.axios !== "function") {
    if (listRoot instanceof HTMLElement) {
      setArticleListError(listRoot, "記事表示ライブラリを読み込めませんでした。");
    }
    if (detailRoot instanceof HTMLElement) {
      showArticleDetailError(detailRoot, "記事表示ライブラリを読み込めませんでした。");
    }
    return;
  }

  if (listRoot instanceof HTMLElement) {
    if (
      typeof window.gridjs !== "object" ||
      window.gridjs === null ||
      typeof window.gridjs.Grid !== "function" ||
      typeof window.gridjs.h !== "function"
    ) {
      setArticleListError(
        listRoot,
        "記事一覧ライブラリを読み込めませんでした。",
      );
      return;
    }
    initializeArticleList(listRoot);
  }
  if (detailRoot instanceof HTMLElement) {
    initializeArticleDetail(detailRoot);
  }
}

initializeArticlePages();
