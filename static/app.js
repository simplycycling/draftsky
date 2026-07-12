const MAX_CHARS = 300;

// --- CSRF ---

// csrfHeaders returns the X-CSRF-Token header (read from the layout <meta> tag)
// for attaching to plain fetch() calls. GET requests are exempt server-side.
function csrfHeaders() {
    const meta = document.querySelector('meta[name="csrf-token"]');
    return meta && meta.content ? { 'X-CSRF-Token': meta.content } : {};
}

// Attach the CSRF token to every HTMX request. Harmless on GET (ignored server-side).
document.body.addEventListener('htmx:configRequest', function(evt) {
    const meta = document.querySelector('meta[name="csrf-token"]');
    if (meta && meta.content) evt.detail.headers['X-CSRF-Token'] = meta.content;
});

function graphemeLength(str) {
    if (typeof Intl !== 'undefined' && Intl.Segmenter) {
        return [...new Intl.Segmenter().segment(str)].length;
    }
    return [...str].length;
}

// --- Composer modal ---

// The composer is in at most one context at a time. Reply and quote are mutually
// exclusive (v1): opening one clears the other, closing clears both.
//   replyContext: { uri, cid, rootUri, rootCid, authorHandle, text }
//   quoteContext: { uri, cid, authorHandle, text }
let replyContext = null;
let quoteContext = null;

// openComposer opens the composer. mode selects the context:
//   openComposer(ctx)            → reply mode (ctx is the reply context) or plain post (ctx null)
//   openComposer(ctx, 'quote')   → quote mode (ctx is the quote context)
// Passing a ctx always clears whichever context is not selected, so the two modes
// can never both be live.
function openComposer(ctx, mode) {
    replyContext = null;
    quoteContext = null;
    // The typeahead cache is scoped to a single composer session — people backspace
    // and retype the same handles constantly within one compose, but a fresh open
    // starts clean so stale suggestions never leak across composes.
    _mentionCache.clear();
    if (ctx && mode === 'quote') {
        quoteContext = ctx;
    } else if (ctx) {
        replyContext = ctx;
    }

    const textarea = document.getElementById('composer-textarea');
    const replyDiv = document.getElementById('composer-reply-context');
    const quoteDiv = document.getElementById('composer-quote-context');

    if (replyContext) {
        document.getElementById('reply-context-author').textContent = '@' + replyContext.authorHandle;
        document.getElementById('reply-context-text').textContent = previewText(replyContext.text);
        replyDiv.style.display = 'block';
        quoteDiv.style.display = 'none';
        textarea.placeholder = 'Write your reply…';
    } else if (quoteContext) {
        document.getElementById('quote-context-author').textContent = '@' + quoteContext.authorHandle;
        document.getElementById('quote-context-text').textContent = previewText(quoteContext.text);
        quoteDiv.style.display = 'block';
        replyDiv.style.display = 'none';
        textarea.placeholder = 'Add a comment';
    } else {
        replyDiv.style.display = 'none';
        quoteDiv.style.display = 'none';
        textarea.placeholder = "What’s up?";
    }

    document.getElementById('composer-overlay').style.display = 'flex';
    textarea.focus();
    loadComposerTemplates();
    updateCounter();
}

// previewText truncates a post body to a compact single-line preview.
function previewText(text) {
    return text.length > 100 ? text.slice(0, 100) + '…' : text;
}

// openComposerReply reads reply data from a post card element's data-* attributes
// and opens the composer in reply mode.
function openComposerReply(el) {
    openComposer({
        uri:          el.dataset.uri,
        cid:          el.dataset.cid,
        rootUri:      el.dataset.rootUri,
        rootCid:      el.dataset.rootCid,
        authorHandle: el.dataset.author,
        text:         el.dataset.text,
    });
}

// openComposerQuote reads quote data from a repost span's data-* attributes and
// opens the composer in quote mode.
function openComposerQuote(el) {
    openComposer({
        uri:          el.dataset.uri,
        cid:          el.dataset.cid,
        authorHandle: el.dataset.author,
        text:         el.dataset.text || '',
    }, 'quote');
}

function closeComposer() {
    replyContext = null;
    quoteContext = null;
    closeMentionDropdown();
    document.getElementById('composer-overlay').style.display = 'none';
    document.getElementById('composer-textarea').value = '';
    document.getElementById('composer-textarea').placeholder = "What’s up?";
    document.getElementById('composer-reply-context').style.display = 'none';
    document.getElementById('composer-quote-context').style.display = 'none';
    document.getElementById('template-select').selectedIndex = 0;
    document.getElementById('suffix-preview').style.display = 'none';
    document.getElementById('suffix-preview').textContent = '';
    document.getElementById('composer-error').style.display = 'none';
    const btn = document.getElementById('composer-post-btn');
    btn.disabled = true;
    btn.textContent = 'Post';
    updateCounter();
}

async function loadComposerTemplates() {
    const sel = document.getElementById('template-select');
    // Reset to just the placeholder option before fetching.
    while (sel.options.length > 1) sel.remove(1);

    try {
        const res = await fetch('/api/composer/templates');
        if (!res.ok) return;
        const templates = await res.json();
        templates.forEach(t => {
            const opt = document.createElement('option');
            opt.value = String(t.id);
            opt.textContent = t.name;
            opt.dataset.suffix = t.suffix;
            sel.appendChild(opt);
        });
    } catch (_) {
        // Non-fatal: user can still post without templates.
    }
}

function selectedOption() {
    const sel = document.getElementById('template-select');
    return sel.options[sel.selectedIndex];
}

// composerState is the pure core of updateCounter: from the raw textarea text, the
// selected template's suffix ('' when none), and whether a quote context is present, it
// computes the combined body+suffix string, the remaining grapheme budget, and whether
// the Post button should be disabled. Extracted so the enable rule is unit-testable
// (jstests/composer_test.js): the load-bearing invariant is that a plain post (or reply)
// with any non-whitespace body text ALWAYS enables — reply mode does not enter here
// because it never changes the rule (only quoteContext relaxes the empty-body case).
function composerState(text, suffix, hasQuoteContext) {
    let combined;
    if (suffix) {
        const normalised = text.replace(/\r\n/g, '\n');
        const trimmed = normalised.replace(/ +$/, '');
        combined = trimmed.endsWith('\n') ? trimmed + suffix : trimmed + ' ' + suffix;
    } else {
        combined = text;
    }
    const remaining = MAX_CHARS - graphemeLength(combined);
    // A bare quote-repost (quote context, empty body) is a valid post, so the Post
    // button enables on quote-context-present even with an empty textarea. Every other
    // mode requires non-whitespace body text.
    const disabled = remaining < 0 || (text.trim() === '' && !hasQuoteContext);
    return { combined, remaining, disabled };
}

function updateCounter() {
    const text = document.getElementById('composer-textarea').value;
    const opt = selectedOption();
    const suffix = (opt && opt.dataset.suffix) ? opt.dataset.suffix : '';
    const { remaining, disabled } = composerState(text, suffix, !!quoteContext);

    const counter = document.getElementById('char-counter');
    counter.textContent = String(remaining);
    counter.className = 'char-counter' +
        (remaining < 0 ? ' over' : remaining < 20 ? ' warn' : '');

    document.getElementById('composer-post-btn').disabled = disabled;
}

function onTemplateChange() {
    const opt = selectedOption();
    const preview = document.getElementById('suffix-preview');
    if (opt && opt.dataset.suffix) {
        preview.textContent = opt.dataset.suffix;
        preview.style.display = 'block';
    } else {
        preview.style.display = 'none';
        preview.textContent = '';
    }
    updateCounter();
}

async function submitPost() {
    const text = document.getElementById('composer-textarea').value.trimStart();
    // Empty text is only valid as a bare quote-repost; otherwise there is nothing to post.
    if (!text && !quoteContext) return;

    const opt = selectedOption();
    const body = { text };
    if (opt && opt.value) {
        body.template_id = parseInt(opt.value, 10);
    }

    // Attach reply refs when the composer is in reply mode.
    const isReply = !!replyContext;
    if (replyContext) {
        const rootUri = replyContext.rootUri || replyContext.uri;
        const rootCid = replyContext.rootCid || replyContext.cid;
        body.reply_parent_uri = replyContext.uri;
        body.reply_parent_cid = replyContext.cid;
        body.reply_root_uri   = rootUri;
        body.reply_root_cid   = rootCid;
    }

    // Attach quote refs when the composer is in quote mode (mutually exclusive with reply).
    if (quoteContext) {
        body.quote_uri = quoteContext.uri;
        body.quote_cid = quoteContext.cid;
    }

    const btn = document.getElementById('composer-post-btn');
    btn.disabled = true;
    btn.textContent = 'Posting…';
    document.getElementById('composer-error').style.display = 'none';

    try {
        const res = await fetch('/api/post', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...csrfHeaders() },
            body: JSON.stringify(body),
        });

        if (res.status === 201) {
            const data = await res.json();
            const hashtags = (opt && opt.dataset.suffix)
                ? [...opt.dataset.suffix.matchAll(/#([^\s]+)/g)].map(m => m[1])
                : [];
            closeComposer();
            if (typeof htmx !== 'undefined') {
                htmx.trigger(document.body, 'postSubmitted', { uri: data.uri, hashtags, isReply });
            }
        } else {
            const data = await res.json().catch(() => ({}));
            showComposerError(data.error || 'Post failed. Please try again.');
            btn.disabled = false;
            btn.textContent = 'Post';
        }
    } catch (_) {
        showComposerError('Network error. Please try again.');
        btn.disabled = false;
        btn.textContent = 'Post';
    }
}

function showComposerError(msg) {
    const el = document.getElementById('composer-error');
    el.textContent = msg;
    el.style.display = 'block';
}

// --- Composer @-mention typeahead ---

// findMentionQuery is the pure token-detection core: given the textarea's full text and
// the caret position, it decides whether the caret sits inside an active @-mention
// token and, if so, returns { query, start } — start being the index of the '@' and
// query the text between '@' and the caret. Returns null when the caret is not inside a
// mention token.
//
// A token starts at an '@' that begins the text OR is immediately preceded by
// whitespace (the same boundary rule as the server-side mention facet regex, so emails
// like foo@bar never trigger), and runs from that '@' to the caret with no intervening
// whitespace. Anything else — caret after a space, '@' mid-word — is not a token.
//
// Purity note: indices here are JS string offsets (UTF-16 code units) into the TEXT.
// They only ever need to agree with the server on the text CONTENT that gets inserted;
// the server independently computes UTF-8 BYTE offsets for facets at post time. An
// emoji earlier in the text shifts these JS indices and the server's byte offsets by
// different amounts, and that is fine — the two never need to match, only the inserted
// "@handle " text does. Exported at the bottom of the file for table tests.
function findMentionQuery(text, caretPos) {
    if (typeof text !== 'string') return null;
    if (caretPos < 0 || caretPos > text.length) return null;
    // Walk back from the caret to the token's '@', bailing the moment we hit whitespace
    // (caret not inside a token) or run off the front (no '@').
    let i = caretPos - 1;
    while (i >= 0) {
        const ch = text[i];
        if (ch === '@') break;
        if (/\s/.test(ch)) return null;
        i--;
    }
    if (i < 0 || text[i] !== '@') return null;
    // Boundary anchor: the '@' must begin the text or follow whitespace. This is what
    // excludes emails (the char before '@' in foo@bar is 'o', not whitespace).
    if (i > 0 && !/\s/.test(text[i - 1])) return null;
    return { query: text.slice(i + 1, caretPos), start: i };
}

// Dropdown state. All confined to the composer's lifetime; reset on close.
let _mentionDropdown = null;      // the anchored <div> or null when closed
let _mentionResults = [];         // current suggestion rows
let _mentionActiveIndex = -1;     // keyboard/hover-highlighted row
const _mentionCache = new Map();  // query → results, cleared on composer open
let _mentionAbort = null;         // AbortController for the in-flight fetch
let _mentionDebounceTimer = null;
let _mentionBlurTimer = null;

// onComposerInput runs on every textarea input: it keeps the character counter honest
// (the suffix/newline separator logic in updateCounter, Gotcha 4, must see every change)
// AND drives mention detection. Replaces the textarea's former oninput=updateCounter.
function onComposerInput() {
    updateCounter();
    handleMentionInput();
}

// handleMentionInput re-evaluates the mention token at the caret after a text change and
// schedules a fetch, or closes the dropdown when there is no active token. The min-1-char
// guard (dropdown waits for the first character after '@') mirrors the fetch guard.
function handleMentionInput() {
    const ta = document.getElementById('composer-textarea');
    if (!ta) return;
    const token = findMentionQuery(ta.value, ta.selectionStart);
    if (!token || token.query.length < 1) {
        closeMentionDropdown();
        return;
    }
    scheduleMentionFetch(token.query);
}

// handleMentionCaretMove re-checks the token when the caret moves WITHOUT editing text
// (click, Arrow/Home/End) while the dropdown is open, so leaving the token closes it and
// moving into a different token refreshes it. No-op when the dropdown is already closed.
function handleMentionCaretMove() {
    if (!_mentionDropdown) return;
    const ta = document.getElementById('composer-textarea');
    if (!ta) return;
    const token = findMentionQuery(ta.value, ta.selectionStart);
    if (!token || token.query.length < 1) {
        closeMentionDropdown();
        return;
    }
    scheduleMentionFetch(token.query);
}

function scheduleMentionFetch(query) {
    clearTimeout(_mentionDebounceTimer);
    // 200ms debounce: typing bursts collapse to one request.
    _mentionDebounceTimer = setTimeout(() => fetchMentionSuggestions(query), 200);
}

async function fetchMentionSuggestions(query) {
    // Cache hit: render straight from memory (no request), covering backspace/retype.
    if (_mentionCache.has(query)) {
        renderMentionDropdown(_mentionCache.get(query), query);
        return;
    }
    // Abort any in-flight request — newer input always wins, so a fast type-then-delete
    // can never leave a stale dropdown from an older query landing late.
    if (_mentionAbort) _mentionAbort.abort();
    _mentionAbort = new AbortController();
    const signal = _mentionAbort.signal;
    try {
        // GET is CSRF-exempt server-side (no token header needed).
        const res = await fetch('/api/actors/typeahead?q=' + encodeURIComponent(query), { signal });
        if (res.status === 401) { closeMentionDropdown(); return; } // expired session — fail silent (poll-stop pattern)
        if (!res.ok) { closeMentionDropdown(); return; }
        const results = await res.json();
        _mentionCache.set(query, results);
        // Guard against a slower request resolving after the token changed: only render
        // if the caret's current token still matches the query we fetched for.
        const ta = document.getElementById('composer-textarea');
        const token = ta ? findMentionQuery(ta.value, ta.selectionStart) : null;
        if (!token || token.query !== query) return;
        renderMentionDropdown(results, query);
    } catch (e) {
        // AbortError is the expected outcome of rapid typing; anything else, close.
        if (e && e.name !== 'AbortError') closeMentionDropdown();
    }
}

function renderMentionDropdown(results, query) {
    if (!Array.isArray(results) || results.length === 0) {
        closeMentionDropdown();
        return;
    }
    _mentionResults = results;
    _mentionActiveIndex = 0;

    let dd = _mentionDropdown;
    if (!dd) {
        dd = document.createElement('div');
        dd.className = 'mention-dropdown';
        dd.id = 'mention-dropdown';
        document.body.appendChild(dd);
        _mentionDropdown = dd;
    }
    dd.innerHTML = '';

    results.forEach((a, idx) => {
        const row = document.createElement('div');
        row.className = 'mention-row' + (idx === 0 ? ' active' : '');
        row.dataset.index = String(idx);

        if (a.avatar) {
            const img = document.createElement('img');
            img.className = 'mention-avatar';
            img.src = a.avatar;
            img.alt = '';
            row.appendChild(img);
        } else {
            const ph = document.createElement('span');
            ph.className = 'mention-avatar mention-avatar-empty';
            row.appendChild(ph);
        }

        const names = document.createElement('span');
        names.className = 'mention-names';
        const disp = document.createElement('span');
        disp.className = 'mention-display';
        disp.textContent = a.display_name || a.handle;
        const handle = document.createElement('span');
        handle.className = 'mention-handle';
        handle.textContent = '@' + a.handle;
        names.appendChild(disp);
        names.appendChild(handle);
        row.appendChild(names);

        // mousedown (not click) preventDefault keeps focus in the textarea so the blur
        // close-timer never fires; the click then inserts.
        row.addEventListener('mousedown', e => e.preventDefault());
        row.addEventListener('mouseenter', () => setMentionActive(idx));
        row.addEventListener('click', e => { e.preventDefault(); insertMention(idx); });

        dd.appendChild(row);
    });

    positionMentionDropdown();
    dd.style.display = 'block';
}

// positionMentionDropdown anchors the dropdown to the textarea's bottom-left corner,
// spanning the textarea's full width.
//
// v1 SIMPLIFICATION: this is deliberately NOT caret-coordinate positioning. Placing the
// dropdown at the caret's x/y inside a <textarea> needs a mirror-div measurement hack
// and is a well-known rabbit hole; full-width-below the textarea is unambiguous and good
// enough. position:fixed against the live textarea rect so it rides the modal regardless
// of nesting (the composer is a fixed overlay; the modal itself does not scroll).
function positionMentionDropdown() {
    const ta = document.getElementById('composer-textarea');
    if (!_mentionDropdown || !ta) return;
    const rect = ta.getBoundingClientRect();
    _mentionDropdown.style.position = 'fixed';
    _mentionDropdown.style.left = rect.left + 'px';
    _mentionDropdown.style.top = rect.bottom + 'px';
    _mentionDropdown.style.width = rect.width + 'px';
}

function setMentionActive(idx) {
    _mentionActiveIndex = idx;
    if (!_mentionDropdown) return;
    Array.from(_mentionDropdown.children).forEach((row, i) => {
        row.classList.toggle('active', i === idx);
    });
}

// onComposerKeydown intercepts navigation keys ONLY while the dropdown is open. Critically
// it preventDefaults Enter (so no newline is inserted into the textarea) and the Arrow
// keys (so the textarea caret does not move) while the list is showing. Escape closes the
// dropdown and stops propagation so the document-level handler doesn't also close the
// whole composer. Left/Right/Home/End are intentionally NOT trapped — they move the caret
// and the keyup handler re-evaluates the token.
function onComposerKeydown(e) {
    if (!_mentionDropdown || _mentionDropdown.style.display === 'none') return;
    switch (e.key) {
        case 'ArrowDown':
            e.preventDefault();
            setMentionActive((_mentionActiveIndex + 1) % _mentionResults.length);
            break;
        case 'ArrowUp':
            e.preventDefault();
            setMentionActive((_mentionActiveIndex - 1 + _mentionResults.length) % _mentionResults.length);
            break;
        case 'Enter':
            e.preventDefault();
            insertMention(_mentionActiveIndex);
            break;
        case 'Escape':
            e.preventDefault();
            e.stopPropagation();
            closeMentionDropdown();
            break;
        default:
            break;
    }
}

// insertMention replaces the active @partial token with "@handle " (trailing space),
// restores the caret after the space, closes the dropdown, and fires an input event so
// updateCounter re-runs over the new text (its suffix/newline separator logic, Gotcha 4,
// is downstream of this edit). The dispatched input re-detects the token at the new caret
// — which sits after a space, so no token — and thus does not re-open the dropdown.
function insertMention(idx) {
    const a = _mentionResults[idx];
    const ta = document.getElementById('composer-textarea');
    if (!a || !ta) { closeMentionDropdown(); return; }
    const caret = ta.selectionStart;
    const token = findMentionQuery(ta.value, caret);
    if (!token) { closeMentionDropdown(); return; }

    const before = ta.value.slice(0, token.start);
    const after = ta.value.slice(caret);
    const insert = '@' + a.handle + ' ';
    ta.value = before + insert + after;
    const newCaret = before.length + insert.length;
    ta.setSelectionRange(newCaret, newCaret);

    closeMentionDropdown();
    ta.focus();
    ta.dispatchEvent(new Event('input', { bubbles: true }));
}

function closeMentionDropdown() {
    clearTimeout(_mentionDebounceTimer);
    if (_mentionAbort) { _mentionAbort.abort(); _mentionAbort = null; }
    if (_mentionDropdown) {
        _mentionDropdown.remove();
        _mentionDropdown = null;
    }
    _mentionResults = [];
    _mentionActiveIndex = -1;
}

// Wire the composer textarea's mention listeners once at load. The composer partial is
// rendered into the layout (before this script), so the textarea exists on every authed
// page. keydown must run in the target phase (before the document Escape/Enter handlers),
// which addEventListener on the element gives us.
(function initMentionTypeahead() {
    const ta = document.getElementById('composer-textarea');
    if (!ta) return;
    ta.addEventListener('keydown', onComposerKeydown);
    ta.addEventListener('keyup', e => {
        // Caret-moving keys we let through in keydown; re-evaluate the token on release.
        if (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') {
            handleMentionCaretMove();
        }
    });
    ta.addEventListener('click', handleMentionCaretMove);
    // Blur closes on a short delay so a row click (which blurs the textarea) lands first.
    ta.addEventListener('blur', () => { _mentionBlurTimer = setTimeout(closeMentionDropdown, 150); });
    ta.addEventListener('focus', () => clearTimeout(_mentionBlurTimer));
})();

// Close composer / repost menu on Escape key. The repost menu takes precedence when
// both are somehow open (it never is — the menu closes before the composer opens).
document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
        if (_hashtagMenu) { closeHashtagMenu(); return; }
        if (_repostMenu) { closeRepostMenu(); return; }
        const overlay = document.getElementById('composer-overlay');
        if (overlay && overlay.style.display !== 'none') closeComposer();
    }
});

// --- Thread navigation ---

// Navigates to the thread view for a post card click, unless the click landed
// on an interactive element that has its own behaviour. .quoted-card and
// .post-video are in the blocklist so that clicking a quote (or a playable
// video) inside the OUTER card doesn't also navigate the outer card — the quote
// has its own navigateToQuoted handler, the video its own play handler.
function navigateToThread(evt, uri) {
    if (!uri) return;
    const blocked = evt.target.closest(
        '.post-count, .post-hashtag, .link-card, .post-image-link, .quoted-card, .post-video, .post-author, a, button'
    );
    if (blocked) return;
    window.location.href = '/thread?uri=' + encodeURIComponent(uri);
}

// navigateToQuoted opens the QUOTED post's own thread when its card is clicked.
// It deliberately does NOT reuse navigateToThread: that function's blocklist
// contains .quoted-card / .post-video precisely to guard the OUTER card, and
// reusing it here made every click inside a quote match .quoted-card (or a
// quoted video match .post-video) and self-block — the quoted-card click-through
// was dead for all quotes. The quote's genuinely interactive children (author
// header → navigateToProfile, quoted images, quoted link cards) already
// stopPropagation, so they never reach here; the non-playing quoted video thumb
// SHOULD reach here and navigate ("play it there"). Only a defensive a/button
// guard remains for any future child that forgets to stopPropagation.
function navigateToQuoted(evt, uri) {
    if (!uri) return;
    if (evt.target.closest('a, button')) return;
    window.location.href = '/thread?uri=' + encodeURIComponent(uri);
}

// navigateToProfile sends the user to a handle's profile view. Wired onto author
// avatars and name/handle blocks; stopPropagation keeps the surrounding card's
// thread navigation (or a notification row's) from also firing. encodeURIComponent
// leaves handle dots intact (rogersherman.com stays un-mangled), and Gin's :actor
// param matches the whole dotted segment.
function navigateToProfile(evt, handle) {
    if (evt) evt.stopPropagation();
    if (!handle) return;
    window.location.href = '/profile/' + encodeURIComponent(handle);
}

// --- Repost / Quote menu ---

// Clicking a repost count opens this small anchored popover instead of reposting
// immediately: it offers Repost (or Undo repost) and Quote. Kept dependency-free —
// a positioned div, not a library. The repost/undo action goes straight to
// /api/repost via fetch and swaps the returned span fragment in place (author/text
// carried forward so quote mode still works after a toggle). Quote opens the composer.
let _repostMenu = null;
let _repostMenuAnchor = null;

// closeRepostMenu removes the open popover and its outside-click listener. No-op when
// none is open.
function closeRepostMenu() {
    if (_repostMenu) {
        _repostMenu.remove();
        _repostMenu = null;
        _repostMenuAnchor = null;
        document.removeEventListener('click', onRepostMenuOutsideClick, true);
    }
}

function onRepostMenuOutsideClick(evt) {
    if (_repostMenu && !_repostMenu.contains(evt.target)) closeRepostMenu();
}

// openRepostMenu builds and positions the popover anchored to the clicked repost
// span. stopPropagation throughout so the card's thread navigation never fires.
function openRepostMenu(evt, el) {
    evt.stopPropagation();
    // A second click on the same anchor toggles the menu closed.
    if (_repostMenu && _repostMenuAnchor === el) {
        closeRepostMenu();
        return;
    }
    closeRepostMenu();

    const reposted = el.dataset.reposted === 'true';
    const menu = document.createElement('div');
    menu.className = 'repost-menu';
    menu.innerHTML =
        `<button type="button" class="repost-menu-item" data-action="repost">${reposted ? 'Undo repost' : 'Repost'}</button>` +
        `<button type="button" class="repost-menu-item" data-action="quote">Quote</button>`;

    menu.querySelector('[data-action="repost"]').addEventListener('click', function(e) {
        e.stopPropagation();
        closeRepostMenu();
        if (reposted) { doUndoRepost(el); } else { doRepost(el); }
    });
    menu.querySelector('[data-action="quote"]').addEventListener('click', function(e) {
        e.stopPropagation();
        closeRepostMenu();
        openComposerQuote(el);
    });

    document.body.appendChild(menu);

    // Anchor just below the span; nudge left so the menu doesn't overflow the viewport.
    const rect = el.getBoundingClientRect();
    let left = rect.left + window.scrollX;
    const top = rect.bottom + window.scrollY + 4;
    const menuWidth = menu.offsetWidth;
    if (left + menuWidth > window.scrollX + document.documentElement.clientWidth - 8) {
        left = window.scrollX + document.documentElement.clientWidth - menuWidth - 8;
    }
    menu.style.left = left + 'px';
    menu.style.top = top + 'px';

    _repostMenu = menu;
    _repostMenuAnchor = el;
    // Capture-phase so the outside-click check runs before other handlers.
    document.addEventListener('click', onRepostMenuOutsideClick, true);
}

// replaceRepostSpan swaps oldEl for the server-returned span fragment, carrying
// data-author/data-text forward (the fragment omits them) so a later Quote still has
// the author handle and quoted text.
function replaceRepostSpan(oldEl, html) {
    const tmp = document.createElement('template');
    tmp.innerHTML = html.trim();
    const newEl = tmp.content.firstElementChild;
    if (!newEl) return;
    newEl.dataset.author = oldEl.dataset.author || '';
    newEl.dataset.text = oldEl.dataset.text || '';
    oldEl.replaceWith(newEl);
}

// doRepost creates a repost and swaps in the fresh span. Form-encoded body matches
// what the handler reads via c.PostForm on POST.
async function doRepost(el) {
    const body = new URLSearchParams({
        uri:   el.dataset.uri,
        cid:   el.dataset.cid,
        count: el.dataset.count || '0',
    });
    try {
        const res = await fetch('/api/repost', {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded', ...csrfHeaders() },
            body,
        });
        if (res.ok) replaceRepostSpan(el, await res.text());
    } catch (_) {
        // Non-fatal: the count simply stays as it was.
    }
}

// doUndoRepost deletes the repost. HTMX's hx-delete sent these as query params, and
// the handler reads c.Query as the fallback — Go's PostForm does not parse a DELETE
// body — so we pass them in the query string too.
async function doUndoRepost(el) {
    const params = new URLSearchParams({
        repost_uri: el.dataset.repostUri,
        post_uri:   el.dataset.uri,
        post_cid:   el.dataset.cid,
        count:      el.dataset.count || '0',
    });
    try {
        const res = await fetch('/api/repost?' + params.toString(), {
            method: 'DELETE',
            headers: { ...csrfHeaders() },
        });
        if (res.ok) replaceRepostSpan(el, await res.text());
    } catch (_) {
        // Non-fatal.
    }
}

// --- Inline video playback ---

// hls.js is self-hosted from our own origin (static/vendor/hls.min.js, v1.6.16).
// A third-party CDN was a reliability liability (outage = no video) and got blocked
// by Brave Shields / ad-blockers; serving from 'self' sidesteps both.
const HLS_JS_SRC = '/static/vendor/hls.min.js';
let _hlsLoaderPromise = null;

// loadHlsJs injects the hls.js <script> on first call and caches the promise so
// subsequent plays reuse the already-loaded library. Resolves with the Hls global.
function loadHlsJs() {
    if (window.Hls) return Promise.resolve(window.Hls);
    if (_hlsLoaderPromise) return _hlsLoaderPromise;
    _hlsLoaderPromise = new Promise((resolve, reject) => {
        const s = document.createElement('script');
        s.src = HLS_JS_SRC;
        s.onload = () => window.Hls ? resolve(window.Hls) : reject(new Error('Hls global missing'));
        s.onerror = () => { _hlsLoaderPromise = null; reject(new Error('failed to load hls.js')); };
        document.head.appendChild(s);
    });
    return _hlsLoaderPromise;
}

// Only one inline video is ever active. These four move as a set: the <video>
// element, its hls.js instance (null on the Safari native path), the .post-video
// container, and the container's original thumbnail + play-overlay markup (kept so
// the container can be reverted to its clickable poster state).
let _activeVideo = null;
let _activeHls = null;
let _activeContainer = null;
let _activeThumbHTML = null;

// teardownActiveVideo stops the active video and fully releases its resources:
// destroys the hls.js instance (freeing the MediaSource and network buffers) or,
// on Safari's native path where there is no hls.js instance, clears the media
// element's src and resets it. It then restores the container to its thumbnail +
// play-overlay markup so re-clicking starts a fresh stream. No-op when idle.
// Safe to call on a container already detached from the DOM (e.g. after a feed
// swap): the innerHTML revert is simply moot in that case.
function teardownActiveVideo() {
    if (!_activeVideo) return;
    _activeVideo.pause();
    if (_activeHls) {
        _activeHls.destroy();
    } else {
        // Safari native HLS: drop the source and reset the element's media state.
        _activeVideo.removeAttribute('src');
        try { _activeVideo.load(); } catch (_) {}
    }
    if (_activeContainer && _activeThumbHTML !== null) {
        _activeContainer.innerHTML = _activeThumbHTML;
        _activeContainer.classList.remove('post-video-playing');
    }
    _activeVideo = null;
    _activeHls = null;
    _activeContainer = null;
    _activeThumbHTML = null;
}

// showVideoError replaces a failed player with a muted "Video failed to load"
// panel instead of an eternal black box. Covers both a fatal hls.js error and a
// media-element 'error' event (native HLS, last-resort src, or a CDN blocked by
// Brave Shields / an ad-blocker). If the failed container is the active one, its
// resources are released first (hls destroyed, active slots cleared) so the panel
// can be re-clicked to retry a fresh stream. The container keeps its data-hls +
// onclick, so clicking the panel calls playInlineVideo again.
function showVideoError(container) {
    if (_activeContainer === container) {
        if (_activeHls) { try { _activeHls.destroy(); } catch (_) {} }
        _activeVideo = null;
        _activeHls = null;
        _activeContainer = null;
        _activeThumbHTML = null;
    }
    container.classList.remove('post-video-playing');
    container.classList.add('post-video-errored');
    container.innerHTML = '<div class="post-video-error">Video failed to load</div>';
}

// playInlineVideo replaces a .post-video thumbnail (identified by its data-hls
// playlist URL) with a <video controls> element and starts playback. Called from
// the thumbnail's onclick, which has already stopped propagation so thread
// navigation doesn't fire. Any previously-playing video is fully torn down first,
// so only one plays at a time and no hls.js instance is ever left orphaned. Native
// HLS (Safari) uses the playlist as src directly; every other browser lazy-loads
// hls.js and attaches via MediaSource.
function playInlineVideo(container) {
    const playlist = container.dataset.hls;
    if (!playlist || container.querySelector('video')) return;

    // Release whatever was playing before; this destroys its hls.js instance and
    // reverts its container, guaranteeing one active stream at a time.
    teardownActiveVideo();

    const thumbHTML = container.innerHTML; // preserved so teardown can revert

    const video = document.createElement('video');
    // LOAD-BEARING — do not remove as "unnecessary". Bluesky's video CDN serves
    // segments/playlists with Access-Control-Allow-Origin, but a non-CORS media
    // fetch (a plain video.src load) caches the response WITHOUT that header. hls.js
    // then issues CORS XHRs for the same URLs, hits those poisoned cache entries,
    // and the browser CORB-blocks them — playback silently dies until the cache is
    // cleared. Forcing crossOrigin here makes even direct-src loads CORS-mode, so
    // every cache entry carries ACAO and hls.js's XHRs are always satisfiable.
    video.crossOrigin = 'anonymous';
    video.controls = true;
    video.playsInline = true;
    video.setAttribute('playsinline', '');
    // Surface a hard media failure (native HLS path, last-resort src, or a CDN
    // blocked by Brave Shields / an ad-blocker) instead of leaving a black box.
    video.addEventListener('error', () => {
        if (_activeVideo === video) showVideoError(container);
    });

    // Swap the thumbnail + play overlay for the video element (identical box).
    container.innerHTML = '';
    container.appendChild(video);
    container.classList.add('post-video-playing');

    // Register as active immediately (hls instance filled in below, if any) so a
    // subsequent play or feed swap can tear this down even before the manifest loads.
    _activeVideo = video;
    _activeHls = null;
    _activeContainer = container;
    _activeThumbHTML = thumbHTML;

    // No autoplay attribute, but the user explicitly clicked, so start playback.
    const startPlayback = () => video.play().catch(() => {});

    // playNative points the media element straight at the playlist, relying on the
    // browser's built-in HLS decoder. This is the LAST resort, not the first choice:
    // Chromium/Brave report canPlayType('application/vnd.apple.mpegurl') === 'maybe'
    // (truthy) but cannot actually decode HLS, so preferring native there sets an
    // unplayable src and playback silently dies. Only browsers that both lack MSE
    // (Hls.isSupported() false) AND claim native support ('probably'/'maybe') take
    // this path — in practice iOS Safari, whose native HLS genuinely works.
    const playNative = () => {
        const nativeSupport = video.canPlayType('application/vnd.apple.mpegurl');
        if (nativeSupport === 'probably' || nativeSupport === 'maybe') {
            video.src = playlist;
            startPlayback();
        } else {
            // Neither MSE nor real native HLS — never assign an unplayable src.
            showVideoError(container);
        }
    };

    // Prefer hls.js (MSE) whenever it can run — this is the standard hls.js
    // integration order and is what desktop Safari uses too when MSE is available.
    // Native HLS is only the fallback when hls.js is unsupported or fails to load.
    loadHlsJs().then(Hls => {
        // Abort only a genuinely stale load: a teardown that fired while hls.js was
        // loading (new play / feed swap) nulls or reassigns _activeVideo, so this
        // element is no longer the active one. On a clean first click _activeVideo
        // was set to this same <video> synchronously above, so the guard passes.
        if (_activeVideo !== video) return;
        if (Hls.isSupported()) {
            const hls = new Hls();
            _activeHls = hls;
            // Fatal hls.js errors (network/media/mux) can't recover — show the error
            // panel rather than a silent, never-buffering black box.
            hls.on(Hls.Events.ERROR, (_evt, data) => {
                if (data && data.fatal) {
                    console.error('hls fatal', data.type, data.details);
                    if (_activeVideo === video) showVideoError(container);
                }
            });
            hls.on(Hls.Events.MANIFEST_PARSED, startPlayback);
            // Attach the media element FIRST, then load the source only once the
            // MediaSource is bound (MEDIA_ATTACHED). Calling loadSource before the
            // media is attached lets hls.js fetch the master + media playlists but
            // leaves the stream controller with no MediaSource to schedule fragments
            // against — the "playlists load, zero fragment requests" hang. Gating
            // loadSource on MEDIA_ATTACHED guarantees fragments are always scheduled.
            hls.on(Hls.Events.MEDIA_ATTACHED, () => hls.loadSource(playlist));
            hls.attachMedia(video);
        } else {
            // No MSE (e.g. iOS Safari): fall back to native HLS if it's real.
            playNative();
        }
    }).catch(() => {
        // hls.js unavailable (offline, blocked): native HLS is the only hope.
        if (_activeVideo !== video) return;
        playNative();
    });
}

// An HTMX feed swap (switching feeds, posting) replaces #feed-root's contents,
// detaching the container that holds a playing video and orphaning its hls.js
// instance. After any swap, if the active video is no longer in the document, tear
// it down to release the MediaSource/network buffers. Append-only swaps (infinite
// scroll) and unrelated swaps (like/repost buttons) leave the video connected, so
// this correctly leaves it playing.
document.body.addEventListener('htmx:afterSwap', function() {
    if (_activeVideo && !_activeVideo.isConnected) teardownActiveVideo();
});

// --- Template management ---

// Updates a character counter span and optionally disables a submit button when over limit.
function updateTrCounter(inputEl, max, counterId, btnId) {
    const remaining = max - [...inputEl.value].length;
    const counter = document.getElementById(counterId);
    if (counter) {
        counter.textContent = String(remaining);
        counter.className = 'char-counter' +
            (remaining < 0 ? ' over' : remaining < 20 ? ' warn' : '');
    }
    if (btnId) {
        const btn = document.getElementById(btnId);
        if (btn) {
            const form = inputEl.closest('form');
            const anyOver = form
                ? Array.from(form.querySelectorAll('.char-counter')).some(el => el.classList.contains('over'))
                : remaining < 0;
            btn.disabled = anyOver;
        }
    }
}

function showEdit(id) {
    const row = document.querySelector(`[data-id="${id}"]`);
    if (row) row.draggable = false;
    document.getElementById('view-' + id).style.display = 'none';
    document.getElementById('edit-' + id).style.display = 'block';
    const nameInput = document.querySelector(`#edit-${id} input[name="name"]`);
    if (nameInput) {
        nameInput.focus();
        updateTrCounter(nameInput, 100, `edit-name-counter-${id}`, `save-btn-${id}`);
    }
    const suffixInput = document.querySelector(`#edit-${id} input[name="suffix"]`);
    if (suffixInput) updateTrCounter(suffixInput, 250, `edit-suffix-counter-${id}`, `save-btn-${id}`);
}

function cancelEdit(id) {
    const row = document.querySelector(`[data-id="${id}"]`);
    if (row) row.draggable = true;
    document.getElementById('view-' + id).style.display = 'flex';
    document.getElementById('edit-' + id).style.display = 'none';
    const err = document.getElementById('edit-error-' + id);
    if (err) { err.style.display = 'none'; err.textContent = ''; }
}

function showDeleteConfirm(id) {
    document.getElementById('view-' + id).style.display = 'none';
    document.getElementById('delete-confirm-' + id).style.display = 'flex';
}

function cancelDeleteConfirm(id) {
    document.getElementById('view-' + id).style.display = 'flex';
    document.getElementById('delete-confirm-' + id).style.display = 'none';
}

function onAddTemplateResponse(evt) {
    const form = document.getElementById('add-template-form');
    const err = document.getElementById('add-template-error');
    if (evt.detail.successful) {
        form.reset();
        err.style.display = 'none';
        err.textContent = '';
        // Reset counters to their starting values after form.reset() clears the inputs.
        const nc = document.getElementById('add-name-counter');
        if (nc) { nc.textContent = '100'; nc.className = 'char-counter'; }
        const sc = document.getElementById('add-suffix-counter');
        if (sc) { sc.textContent = '250'; sc.className = 'char-counter'; }
        const addBtn = document.getElementById('add-btn');
        if (addBtn) addBtn.disabled = false;
        // Remove empty-state placeholder if present
        const emptyState = document.getElementById('empty-state');
        if (emptyState) emptyState.remove();
    } else {
        try {
            const d = JSON.parse(evt.detail.xhr.responseText);
            err.textContent = d.error || 'Failed to create template';
        } catch (_) {
            err.textContent = 'Failed to create template';
        }
        err.style.display = 'block';
    }
}

function onEditResponse(evt, id) {
    if (!evt.detail.successful) {
        const err = document.getElementById('edit-error-' + id);
        if (err) {
            try {
                const d = JSON.parse(evt.detail.xhr.responseText);
                err.textContent = d.error || 'Failed to save';
            } catch (_) {
                err.textContent = 'Failed to save';
            }
            err.style.display = 'block';
        }
    }
}

// --- Drag-and-drop reorder ---

let _dragSrc = null;

function onDragStart(evt) {
    _dragSrc = evt.currentTarget;
    evt.dataTransfer.effectAllowed = 'move';
    evt.dataTransfer.setData('text/plain', _dragSrc.dataset.id);
    setTimeout(() => _dragSrc.classList.add('dragging'), 0);
}

function onDragOver(evt) {
    evt.preventDefault();
    evt.dataTransfer.dropEffect = 'move';
    const target = evt.currentTarget;
    if (target !== _dragSrc) {
        document.querySelectorAll('.template-row').forEach(r => r.classList.remove('drag-over'));
        target.classList.add('drag-over');
    }
    return false;
}

function onDragEnd(evt) {
    if (_dragSrc) _dragSrc.classList.remove('dragging');
    document.querySelectorAll('.template-row').forEach(r => r.classList.remove('drag-over'));
}

function onDrop(evt) {
    evt.stopPropagation();
    evt.preventDefault();
    const target = evt.currentTarget;
    target.classList.remove('drag-over');
    if (!_dragSrc || _dragSrc === target) return;

    const list = document.getElementById('template-list');
    const rows = Array.from(list.querySelectorAll('.template-row'));
    const srcIdx = rows.indexOf(_dragSrc);
    const tgtIdx = rows.indexOf(target);

    if (srcIdx < tgtIdx) {
        target.after(_dragSrc);
    } else {
        target.before(_dragSrc);
    }

    saveReorder();
}

async function saveReorder() {
    const list = document.getElementById('template-list');
    if (!list) return;
    const ids = Array.from(list.querySelectorAll('.template-row'))
        .map(row => parseInt(row.dataset.id, 10));

    try {
        const res = await fetch('/api/templates/reorder', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', ...csrfHeaders() },
            body: JSON.stringify({ ids }),
        });
        if (res.ok) {
            list.classList.add('reorder-saved');
            setTimeout(() => list.classList.remove('reorder-saved'), 1200);
        }
    } catch (_) {
        // Non-fatal: DOM is already updated; server will re-sync on next load
    }
}

// --- Feed switching ---

// Tracks the URL of the currently displayed feed so reply-without-hashtags
// can refresh the same feed rather than jumping to Following.
let currentFeedURL = '/feed/following';

// Removes active from all feed tabs; marks btn active when non-null.
function activateTab(btn) {
    document.querySelectorAll('.feed-tab').forEach(t => t.classList.remove('active'));
    if (btn) btn.classList.add('active');
}

// Activates a feed tab, updates currentFeedURL, and swaps the centre feed via HTMX.
function switchFeedTab(btn, feedURL) {
    activateTab(btn);
    currentFeedURL = feedURL;
    htmx.ajax('GET', feedURL, { target: '#feed-root', swap: 'innerHTML' });
}

function switchToHashtagFeed(tags) {
    if (!tags || tags.length === 0) {
        // No tags — return to Following feed and re-activate that tab.
        switchFeedTab(document.querySelector('.feed-tab[data-timeline]'), '/feed/following');
        return;
    }
    activateTab(null); // hashtag feed has no pinned tab
    currentFeedURL = '/feed/hashtags?tags=' + tags.map(encodeURIComponent).join(',');
    htmx.ajax('GET', currentFeedURL, { target: '#feed-root', swap: 'innerHTML' });
}

// switchToHashtagFeedByAuthor loads the "#tag posts by @author" feed — the same
// hashtag route with an author filter (searchPosts resolves the handle to a DID
// server-side). The server validates author and renders the "by @author" controls
// label; the existing Back button returns to Following unchanged.
function switchToHashtagFeedByAuthor(tag, author) {
    if (!tag) return;
    if (!author) { switchToHashtagFeed([tag]); return; }
    activateTab(null); // hashtag feed has no pinned tab
    currentFeedURL = '/feed/hashtags?tags=' + encodeURIComponent(tag) +
        '&author=' + encodeURIComponent(author);
    htmx.ajax('GET', currentFeedURL, { target: '#feed-root', swap: 'innerHTML' });
}

// --- Hashtag context menu ---

// Clicking a hashtag span opens this small anchored popover (Bluesky-style) rather
// than switching feeds immediately. It offers "See #tag posts" (the merged hashtag
// feed) and, when the hashtag belongs to a post with a known author, "See #tag posts
// by @author" (the author-filtered feed). In author-less contexts — profile bios —
// only the first option renders. The popover mirrors the repost menu exactly: a
// positioned div, capture-phase outside-click close, Escape close, stopPropagation
// throughout, addEventListener only (no hx-on — dead under our CSP, Gotcha 17).
//
// Deliberate asymmetry: the right-rail recent tags (layout.html) keep calling
// switchToHashtagFeed directly — they are YOUR own tags, so a "by @you" option is
// noise; a menu there would be pure friction.
let _hashtagMenu = null;
let _hashtagMenuAnchor = null;

function closeHashtagMenu() {
    if (_hashtagMenu) {
        _hashtagMenu.remove();
        _hashtagMenu = null;
        _hashtagMenuAnchor = null;
        document.removeEventListener('click', onHashtagMenuOutsideClick, true);
    }
}

function onHashtagMenuOutsideClick(evt) {
    if (_hashtagMenu && !_hashtagMenu.contains(evt.target)) closeHashtagMenu();
}

// openHashtagMenu(evt, tag, authorHandle) — tag is bare (no '#'); authorHandle may be
// empty. Text nodes are built with textContent (never innerHTML) so a hashtag or
// handle can never inject markup.
function openHashtagMenu(evt, tag, authorHandle) {
    evt.stopPropagation();
    // A second click on the same hashtag toggles the menu closed.
    if (_hashtagMenu && _hashtagMenuAnchor === evt.currentTarget) {
        closeHashtagMenu();
        return;
    }
    closeHashtagMenu();

    const anchor = evt.currentTarget;
    const menu = document.createElement('div');
    menu.className = 'hashtag-menu';

    const seePosts = document.createElement('button');
    seePosts.type = 'button';
    seePosts.className = 'hashtag-menu-item';
    seePosts.textContent = 'See #' + tag + ' posts';
    seePosts.addEventListener('click', function(e) {
        e.stopPropagation();
        closeHashtagMenu();
        switchToHashtagFeed([tag]);
    });
    menu.appendChild(seePosts);

    if (authorHandle) {
        const byAuthor = document.createElement('button');
        byAuthor.type = 'button';
        byAuthor.className = 'hashtag-menu-item';
        byAuthor.textContent = 'See #' + tag + ' posts by @' + authorHandle;
        byAuthor.addEventListener('click', function(e) {
            e.stopPropagation();
            closeHashtagMenu();
            switchToHashtagFeedByAuthor(tag, authorHandle);
        });
        menu.appendChild(byAuthor);
    }

    document.body.appendChild(menu);

    // Anchor just below the hashtag; nudge left so the menu doesn't overflow the viewport.
    const rect = anchor.getBoundingClientRect();
    let left = rect.left + window.scrollX;
    const top = rect.bottom + window.scrollY + 4;
    const menuWidth = menu.offsetWidth;
    if (left + menuWidth > window.scrollX + document.documentElement.clientWidth - 8) {
        left = window.scrollX + document.documentElement.clientWidth - menuWidth - 8;
    }
    menu.style.left = left + 'px';
    menu.style.top = top + 'px';

    _hashtagMenu = menu;
    _hashtagMenuAnchor = anchor;
    document.addEventListener('click', onHashtagMenuOutsideClick, true);
}

// --- Settings: theme selector ---

const THEME_KEYS = ['ocean', 'slate', 'amber', 'graphite'];

// applyTheme swaps the live theme without a reload. The CSS keys themes off a
// body class (:root holds the ocean defaults, body.slate/.amber/.graphite
// override), so 'ocean' means removing every theme class and adding none.
function applyTheme(themeKey) {
    document.body.classList.remove(...THEME_KEYS);
    if (themeKey !== 'ocean') document.body.classList.add(themeKey);

    // Move the selected outline to the chosen card.
    document.querySelectorAll('.theme-card').forEach(card => {
        card.classList.toggle('selected', card.dataset.themeKey === themeKey);
    });
}

// Single delegated htmx:afterRequest dispatcher for every post-request DOM hook.
// These MUST NOT be hx-on::after-request attributes: HTMX evaluates hx-on bodies
// with new Function(), which the app's CSP (no 'unsafe-eval') blocks. The failure
// is silent — a CSP EvalError lands in the console but HTMX carries on, the swap
// still happens, and only the handler is skipped (see Gotcha 17 in CLAUDE.md). So
// every hook lives here, keyed off the requesting element (evt.detail.elt). The
// request itself is still driven by the element's hx-* attributes in the HTML.
document.body.addEventListener('htmx:afterRequest', function(evt) {
    const elt = evt.detail.elt;
    if (!elt) return;

    // Template Add form — clears inputs on success, renders the inline 409/400
    // error on failure.
    if (elt.id === 'add-template-form') {
        onAddTemplateResponse(evt);
        return;
    }

    // Template inline-edit form — renders the inline error on failure (a success
    // swaps the whole row via hx-swap="outerHTML", so nothing to do there).
    if (elt.matches && elt.matches('[data-edit-template-id]')) {
        onEditResponse(evt, elt.dataset.editTemplateId);
        return;
    }

    // Settings theme card — repaints the theme instantly on success; on any error
    // (e.g. a 403 the UI didn't expect) the current theme is left untouched.
    const card = elt.closest ? elt.closest('.theme-card') : null;
    if (card && evt.detail.successful) {
        applyTheme(card.dataset.themeKey);
    }
});

// showThemeLocked reveals the one-line paid-feature note when a free user clicks
// a locked theme card. No modal, no upsell — just the sentence.
function showThemeLocked() {
    const msg = document.getElementById('theme-locked-msg');
    if (msg) msg.style.display = '';
}

document.body.addEventListener('postSubmitted', function(evt) {
    const { hashtags, isReply } = evt.detail;
    if (hashtags && hashtags.length > 0) {
        // Post (or reply) with hashtags — switch to merged hashtag feed.
        switchToHashtagFeed(hashtags);
    } else if (isReply) {
        // Reply without hashtags — refresh wherever the user currently is.
        htmx.ajax('GET', currentFeedURL, { target: '#feed-root', swap: 'innerHTML' });
    } else {
        // Regular post with no hashtags — go to Following feed.
        switchToHashtagFeed([]);
    }
});

// --- Notification badge polling ---

// Poll /api/notifications/unread-count so the left-rail badge updates while the app
// is open, not only at page-render time. 60s is responsive enough for a badge without
// leaning on the PDS. Background tabs are skipped (document.hidden) and a poll fires
// immediately when the tab regains focus, so returning to it feels instant. A 401
// (expired session) stops polling silently rather than spamming failed requests.
let notifPollTimer = null;
let notifPollStopped = false;

// updateNotificationBadge reconciles the left-rail pill with count: create/show it
// above zero, update the number, remove it at zero. Mirrors the server-rendered
// markup (a .nav-badge span appended inside .nav-link-notifications).
function updateNotificationBadge(count) {
    const link = document.querySelector('.nav-link-notifications');
    if (!link) return;
    let badge = link.querySelector('.nav-badge');
    if (count > 0) {
        if (!badge) {
            badge = document.createElement('span');
            badge.className = 'nav-badge';
            link.appendChild(badge);
        }
        badge.textContent = String(count);
    } else if (badge) {
        badge.remove();
    }
}

function stopNotificationPolling() {
    notifPollStopped = true;
    if (notifPollTimer !== null) {
        clearInterval(notifPollTimer);
        notifPollTimer = null;
    }
}

async function pollUnreadCount() {
    // Skip once stopped (401) and while the tab is hidden — no point polling a
    // background tab. The interval keeps ticking but makes no request when hidden,
    // so the network tab shows nothing until the tab is focused again.
    if (notifPollStopped || document.hidden) return;
    try {
        // GET is CSRF-exempt server-side, so no token header is needed.
        const res = await fetch('/api/notifications/unread-count');
        if (res.status === 401) {
            stopNotificationPolling();
            return;
        }
        if (!res.ok) return; // transient error — next tick retries
        const data = await res.json();
        updateNotificationBadge(data.count || 0);
    } catch (_) {
        // Network hiccup — non-fatal; the next tick retries.
    }
}

// Start polling only on authenticated pages (those with the notifications nav link).
// No immediate poll on load: the server already rendered the correct count. Fires
// an immediate poll when the tab becomes visible so a returning user sees fresh state.
(function initNotificationPolling() {
    if (!document.querySelector('.nav-link-notifications')) return;
    notifPollTimer = setInterval(pollUnreadCount, 60000);
    document.addEventListener('visibilitychange', function() {
        if (!document.hidden) pollUnreadCount();
    });
})();

// --- Profile editing (own profile only) ---

// Profile field limits mirror the server-side rune caps in profile.go. Counting is
// by code point ([...str].length), matching the template-form counters — server
// validation is the enforcement; these are UX only.
const PROFILE_NAME_MAX = 64;
const PROFILE_BIO_MAX = 256;

function showProfileEdit() {
    const btn = document.getElementById('profile-edit-btn');
    const form = document.getElementById('profile-edit-form');
    if (btn) btn.style.display = 'none';
    if (form) {
        form.style.display = 'block';
        updateProfileCounters();
        const nameInput = document.getElementById('profile-edit-name');
        if (nameInput) nameInput.focus();
    }
}

function cancelProfileEdit() {
    const btn = document.getElementById('profile-edit-btn');
    const form = document.getElementById('profile-edit-form');
    const err = document.getElementById('profile-edit-error');
    if (form) form.style.display = 'none';
    if (btn) btn.style.display = '';
    if (err) { err.style.display = 'none'; err.textContent = ''; }
}

function updateProfileCounters() {
    const nameInput = document.getElementById('profile-edit-name');
    const bioInput = document.getElementById('profile-edit-bio');
    const nameCounter = document.getElementById('profile-name-counter');
    const bioCounter = document.getElementById('profile-bio-counter');
    let over = false;
    if (nameInput && nameCounter) {
        const rem = PROFILE_NAME_MAX - [...nameInput.value].length;
        nameCounter.textContent = String(rem);
        nameCounter.className = 'char-counter' + (rem < 0 ? ' over' : rem < 10 ? ' warn' : '');
        if (rem < 0) over = true;
    }
    if (bioInput && bioCounter) {
        const rem = PROFILE_BIO_MAX - [...bioInput.value].length;
        bioCounter.textContent = String(rem);
        bioCounter.className = 'char-counter' + (rem < 0 ? ' over' : rem < 20 ? ' warn' : '');
        if (rem < 0) over = true;
    }
    const saveBtn = document.getElementById('profile-save-btn');
    if (saveBtn) saveBtn.disabled = over;
}

// submitProfileEdit PUTs the display name + bio to /api/profile (form-encoded, matching
// the handler's c.PostForm reads) and reloads on success so the header re-renders from
// the fresh server view — the reload is also the avatar-survival check: getProfile
// returns the preserved avatar/banner.
async function submitProfileEdit(evt) {
    evt.preventDefault();
    const nameInput = document.getElementById('profile-edit-name');
    const bioInput = document.getElementById('profile-edit-bio');
    const err = document.getElementById('profile-edit-error');
    const saveBtn = document.getElementById('profile-save-btn');

    const body = new URLSearchParams({
        display_name: nameInput ? nameInput.value : '',
        description:  bioInput ? bioInput.value : '',
    });

    if (saveBtn) { saveBtn.disabled = true; saveBtn.textContent = 'Saving…'; }
    if (err) { err.style.display = 'none'; err.textContent = ''; }

    try {
        const res = await fetch('/api/profile', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded', ...csrfHeaders() },
            body,
        });
        if (res.ok) {
            window.location.reload();
            return;
        }
        const data = await res.json().catch(() => ({}));
        if (err) { err.textContent = data.error || 'Failed to save profile.'; err.style.display = 'block'; }
    } catch (_) {
        if (err) { err.textContent = 'Network error. Please try again.'; err.style.display = 'block'; }
    }
    if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = 'Save'; }
}

// --- Follow / unfollow (others' profiles) ---

// toggleFollow flips the follow state via /api/follow, mirroring the like/repost record
// pattern: create returns the new follow record URI (needed to unfollow), delete clears
// it. The button updates optimistically and reconciles on response.
async function toggleFollow(btn) {
    const following = btn.dataset.following === 'true';
    btn.disabled = true;
    try {
        if (following) {
            const params = new URLSearchParams({ follow_uri: btn.dataset.followUri || '' });
            const res = await fetch('/api/follow?' + params.toString(), {
                method: 'DELETE',
                headers: { ...csrfHeaders() },
            });
            if (res.ok) {
                btn.dataset.following = 'false';
                btn.dataset.followUri = '';
                btn.classList.remove('following');
                btn.textContent = 'Follow';
            }
        } else {
            const body = new URLSearchParams({ did: btn.dataset.did || '' });
            const res = await fetch('/api/follow', {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded', ...csrfHeaders() },
                body,
            });
            if (res.ok) {
                const data = await res.json().catch(() => ({}));
                btn.dataset.following = 'true';
                btn.dataset.followUri = data.follow_uri || '';
                btn.classList.add('following');
                btn.textContent = 'Following';
            }
        }
    } catch (_) {
        // Non-fatal: the button state simply stays as it was.
    }
    btn.disabled = false;
}

// Export the pure token-detection function for Node table tests (node:test). The guard
// keeps this a no-op in the browser, where `module` is undefined — app.js stays a plain
// non-module script served straight to the page.
if (typeof module !== 'undefined' && module.exports) {
    module.exports = { findMentionQuery, composerState };
}
