const MAX_CHARS = 300;

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
            headers: { 'Content-Type': 'application/json' },
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
            headers: { 'Content-Type': 'application/json' },
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

function switchToHashtagFeed(tags) {
    currentFeedURL = (tags && tags.length > 0)
        ? '/feed/hashtags?tags=' + tags.map(encodeURIComponent).join(',')
        : '/feed/following';
    htmx.ajax('GET', currentFeedURL, { target: '#feed-root', swap: 'innerHTML' });
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
