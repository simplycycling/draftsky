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

// Holds reply context when the composer is open in reply mode; null otherwise.
let replyContext = null;

// openComposer opens the composer. Pass a reply context object to enter reply mode:
//   { uri, cid, rootUri, rootCid, authorHandle, text }
// Call with no argument (or null) for a normal post.
function openComposer(ctx) {
    replyContext = ctx || null;

    const textarea = document.getElementById('composer-textarea');
    const replyDiv = document.getElementById('composer-reply-context');

    if (replyContext) {
        document.getElementById('reply-context-author').textContent = '@' + replyContext.authorHandle;
        const preview = replyContext.text.length > 100
            ? replyContext.text.slice(0, 100) + '…'
            : replyContext.text;
        document.getElementById('reply-context-text').textContent = preview;
        replyDiv.style.display = 'block';
        textarea.placeholder = 'Write your reply…';
    } else {
        replyDiv.style.display = 'none';
        textarea.placeholder = "What’s up?";
    }

    document.getElementById('composer-overlay').style.display = 'flex';
    textarea.focus();
    loadComposerTemplates();
    updateCounter();
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

function closeComposer() {
    replyContext = null;
    document.getElementById('composer-overlay').style.display = 'none';
    document.getElementById('composer-textarea').value = '';
    document.getElementById('composer-textarea').placeholder = "What’s up?";
    document.getElementById('composer-reply-context').style.display = 'none';
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

function updateCounter() {
    const text = document.getElementById('composer-textarea').value;
    const opt = selectedOption();
    const suffix = (opt && opt.dataset.suffix) ? opt.dataset.suffix : '';
    let combined;
    if (suffix) {
        const normalised = text.replace(/\r\n/g, '\n');
        const trimmed = normalised.replace(/ +$/, '');
        combined = trimmed.endsWith('\n') ? trimmed + suffix : trimmed + ' ' + suffix;
    } else {
        combined = text;
    }
    const remaining = MAX_CHARS - graphemeLength(combined);

    const counter = document.getElementById('char-counter');
    counter.textContent = String(remaining);
    counter.className = 'char-counter' +
        (remaining < 0 ? ' over' : remaining < 20 ? ' warn' : '');

    const btn = document.getElementById('composer-post-btn');
    btn.disabled = remaining < 0 || text.trim() === '';
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
    if (!text) return;

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

// Close composer on Escape key.
document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
        const overlay = document.getElementById('composer-overlay');
        if (overlay && overlay.style.display !== 'none') closeComposer();
    }
});

// --- Thread navigation ---

// Navigates to the thread view for a post card click, unless the click landed
// on an interactive element that has its own behaviour.
function navigateToThread(evt, uri) {
    if (!uri) return;
    const blocked = evt.target.closest(
        '.post-count, .post-hashtag, .link-card, .post-image-link, .quoted-card, .post-video, a, button'
    );
    if (blocked) return;
    window.location.href = '/thread?uri=' + encodeURIComponent(uri);
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
