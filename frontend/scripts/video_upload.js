const dropZone    = document.getElementById('dropZone');
const fileInput   = document.getElementById('fileInput');
const preview     = document.getElementById('preview');
const progressBar = document.getElementById('progressBar');
const statusEl    = document.getElementById('status');

let sessionID = null;

// Prevent default drag behaviors
['dragenter','dragover','dragleave','drop'].forEach(evt =>
dropZone.addEventListener(evt, e => {
    e.preventDefault();
    e.stopPropagation();
})
);

// Highlight drop zone
dropZone.addEventListener('dragover', () => dropZone.classList.add('dragover'));
dropZone.addEventListener('dragleave', () => dropZone.classList.remove('dragover'));

// Handle file drop
dropZone.addEventListener('drop', e => {
dropZone.classList.remove('dragover');
const file = e.dataTransfer.files[0];
handleFile(file);
});

// Handle file picker
fileInput.addEventListener('change', () => {
const file = fileInput.files[0];
handleFile(file);
});

function handleFile(file) {
if (!file) return;
// Show video preview
preview.src = URL.createObjectURL(file);
preview.load();
preview.onloadedmetadata = () => {
    statusEl.textContent = `Preview: ${file.name} (${(file.size/1024/1024).toFixed(2)} MB)`;
};

// Generate sessionID once (or set from elsewhere)
if (!sessionID) {
    sessionID = generateSessionID();
}

// Kick off the upload
uploadFile(file);
}

function uploadFile(file) {
const url = `http://localhost:8082/api/video/${sessionID}`;
const xhr = new XMLHttpRequest();

xhr.open('POST', url, true);
xhr.setRequestHeader('Accept', 'application/json');

// Progress event
xhr.upload.onprogress = e => {
    if (e.lengthComputable) {
    const pct = Math.round((e.loaded / e.total) * 100);
    progressBar.style.width = pct + '%';
    statusEl.textContent = `Uploading… ${pct}%`;
    }
};

// Completion handler
xhr.onload = () => {
    if (xhr.status === 201) {
    const resp = JSON.parse(xhr.responseText);
    // fire a DOM event so any part of your code can react:
    window.dispatchEvent(
        new CustomEvent('videoUploaded', { detail: resp })
    );
    } else {
    statusEl.textContent = `Upload failed (${xhr.status}): ${xhr.statusText}`;
    }
};

xhr.onerror = () => {
    statusEl.textContent = 'Upload error. Check console for details.';
};

// Build form data
const form = new FormData();
form.append('video', file, file.name);
xhr.send(form);
}

// Very simple random ID; swap in uuid.js if you like
function generateSessionID() {
return 'sess-' + Math.random().toString(36).substr(2, 9);
}

// 1) Listen for our custom event anywhere in your app:
window.addEventListener('videoUploaded', e => {
  const { sessionID, fileName, path } = e.detail;
  console.log('🎉 upload finished:', e.detail);
  statusEl.textContent = `Upload complete: ${fileName}`;
  // now you can kick off HLS-segmentation or navigate to the player…
});