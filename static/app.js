const MAX_CHARS = 300;

function graphemeLength(str) {
    if (typeof Intl !== 'undefined' && Intl.Segmenter) {
        return [...new Intl.Segmenter().segment(str)].length;
    }
    return [...str].length;
}

// --- Composer modal ---

function openComposer() {
    document.getElementById('composer-overlay').style.display = 'flex';
    document.getElementById('composer-textarea').focus();
    loadComposerTemplates();
    updateCounter();
}

function closeComposer() {
    document.getElementById('composer-overlay').style.display = 'none';
    document.getElementById('composer-textarea').value = '';
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
    const combined = suffix ? text + ' ' + suffix : text;
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
    const text = document.getElementById('composer-textarea').value.trim();
    if (!text) return;

    const opt = selectedOption();
    const body = { text };
    if (opt && opt.value) {
        body.template_id = parseInt(opt.value, 10);
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
                htmx.trigger(document.body, 'postSubmitted', { uri: data.uri, hashtags });
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
