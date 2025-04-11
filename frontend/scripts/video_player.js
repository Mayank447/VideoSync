const videoElement = document.getElementById('videoPlayer');
const sessionKey = new URLSearchParams(window.location.search).get('sessionKey');
let isHost = false;
let ws = null;

// Initialize WebSocket connection
function connectWebSocket() {
    ws = new WebSocket(`wss://your-api-domain.com/ws?sessionKey=${sessionKey}`);

    ws.onopen = () => {
        console.log('WebSocket connected');
        setStatus('Connected to session');
        checkUserRole();
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        handleSyncMessage(data);
    };

    ws.onclose = () => {
        console.log('WebSocket disconnected');
        setStatus('Connection lost - attempting to reconnect...', true);
        setTimeout(connectWebSocket, 3000);
    };
}

// Check if user is host
async function checkUserRole() {
    try {
        const response = await fetch(`/api/sessions/${sessionKey}/role`);
        const { isHost: hostStatus } = await response.json();
        
        isHost = hostStatus;
        document.getElementById('userRole').textContent = isHost ? 'Host' : 'Participant';
        updateControls();
    } catch (error) {
        console.error('Role check failed:', error);
    }
}

// Update UI controls based on role
function updateControls() {
    const controls = document.querySelectorAll('.controls button, #seekBar');
    controls.forEach(control => {
        control.disabled = !isHost;
    });
}

// Handle synchronization messages
function handleSyncMessage(data) {
    switch (data.type) {
        case 'stateUpdate':
            handleStateUpdate(data.payload);
            break;
        case 'heartbeat':
            sendHeartbeatAck();
            break;
        case 'metadata':
            initializeVideo(data.payload);
            break;
    }
}

// Initialize video player with metadata
function initializeVideo(metadata) {
    videoElement.src = metadata.videoUrl;
    videoElement.load();
    
    // Use HLS.js for HLS streaming if needed
    if (metadata.videoUrl.endsWith('.m3u8')) {
        const hls = new Hls();
        hls.loadSource(metadata.videoUrl);
        hls.attachMedia(videoElement);
    }
}

// Handle play/pause/sync commands
function handleStateUpdate(state) {
    const currentTime = videoElement.currentTime * 1000;
    
    if (state.paused !== videoElement.paused) {
        state.paused ? videoElement.pause() : videoElement.play();
    }

    if (Math.abs(currentTime - state.currentTime) > 1000) {
        videoElement.currentTime = state.currentTime / 1000;
    }
}

// Send player events to server (host only)
function sendPlayerEvent(type, value) {
    if (!isHost || !ws) return;

    ws.send(JSON.stringify({
        type: 'playerEvent',
        payload: {
            eventType: type,
            value: value,
            timestamp: Date.now()
        }
    }));
}

// UI event listeners
videoElement.addEventListener('play', () => sendPlayerEvent('play'));
videoElement.addEventListener('pause', () => sendPlayerEvent('pause'));
videoElement.addEventListener('seeked', () => {
    sendPlayerEvent('seek', videoElement.currentTime * 1000);
});

// Update time display
videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = 
        formatTime(videoElement.currentTime);
    document.getElementById('seekBar').value = videoElement.currentTime;
});

// Format time helper
function formatTime(seconds) {
    const date = new Date(0);
    date.setSeconds(seconds);
    return date.toISOString().substr(11, 8);
}

// Status message display
function setStatus(message, isError = false) {
    const statusElement = document.getElementById('statusMessage');
    statusElement.textContent = message;
    statusElement.style.color = isError ? '#dc3545' : '#28a745';
}

// Initialization
document.getElementById('sessionKeyDisplay').textContent = sessionKey;
connectWebSocket();