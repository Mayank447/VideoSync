const dropZone = document.getElementById('dropZone');
const fileInput = document.getElementById('fileInput');
const preview = document.getElementById('preview');
const progressBar = document.getElementById('progressBar');
const progressContainer = document.querySelector('.progress-container');
const statusDiv = document.getElementById('status');

// Prevent default drag behaviors
['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {
    dropZone.addEventListener(eventName, preventDefaults, false);
    document.body.addEventListener(eventName, preventDefaults, false);
});

// Highlight drop zone when item is dragged over it
['dragenter', 'dragover'].forEach(eventName => {
    dropZone.addEventListener(eventName, highlight, false);
});

['dragleave', 'drop'].forEach(eventName => {
    dropZone.addEventListener(eventName, unhighlight, false);
});

// Handle dropped files
dropZone.addEventListener('drop', handleDrop, false);
fileInput.addEventListener('change', handleFileSelect, false);

function preventDefaults(e) {
    e.preventDefault();
    e.stopPropagation();
}

function highlight(e) {
    dropZone.classList.add('dragover');
}

function unhighlight(e) {
    dropZone.classList.remove('dragover');
}

function handleDrop(e) {
    const dt = e.dataTransfer;
    const files = dt.files;
    handleFiles(files);
}

function handleFileSelect(e) {
    const files = e.target.files;
    handleFiles(files);
}

function handleFiles(files) {
    const file = files[0];

if (!file.type.startsWith('video/')) {
    showStatus('Please upload a video file', 'error');
    return;
}

// Show preview
preview.style.display = 'block';
preview.src = URL.createObjectURL(file);

// Start upload
uploadFile(file);
}

function uploadFile(file) {
    const formData = new FormData();
    formData.append('video', file);

    const xhr = new XMLHttpRequest();

    xhr.upload.addEventListener('progress', e => {
        if (e.lengthComputable) {
            const percent = (e.loaded / e.total) * 100;
            progressBar.style.width = `${percent}%`;
            progressContainer.style.display = 'block';
        }
    });

    xhr.onreadystatechange = () => {
    if (xhr.readyState === XMLHttpRequest.DONE) {
        progressContainer.style.display = 'none';
        
        if (xhr.status === 200) {
            const response = JSON.parse(xhr.responseText);
            showStatus(`Upload successful! Video ID: ${response.videoId}`, 'success');
            // You can add redirect logic here
        } else {
            showStatus(`Upload failed: ${xhr.responseText}`, 'error');
        }
    }
};

xhr.open('POST', 'http:localhost:8090/api/upload', true); // Replace with your upload endpoint
xhr.send(formData);
}

function showStatus(message, type) {
    statusDiv.style.display = 'block';
    statusDiv.className = type;
    statusDiv.textContent = message;
}