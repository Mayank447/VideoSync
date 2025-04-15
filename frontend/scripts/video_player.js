const BACKEND_URL = 'http://localhost:8080';

const videoElement = document.getElementById('videoPlayer');
const urlParams = new URLSearchParams(window.location.search);
const sessionKey = urlParams.get('sessionKey');
let isHost = false;
let ws = null;
let latency = 0;

// Initialize WebSocket connection
function connectWebSocket() {
    ws = new WebSocket(`${BACKEND_URL.replace('http', 'ws')}/ws?sessionKey=${sessionKey}`);

    ws.onopen = () => {
        console.log('WebSocket connected');
        setStatus('Connected to session', false);
        loadVideo();
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

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        setStatus('Connection error', true);
    };
}

// Load video from backend
async function loadVideo() {
    try {
        videoElement.src = `${BACKEND_URL}/api/video`;
        videoElement.load();
    } catch (error) {
        console.error('Error loading video:', error);
        setStatus('Error loading video', true);
    }
}

// Handle synchronization messages
function handleSyncMessage(data) {
    switch (data.type) {
        case 'init':
            handleInitialization(data);
            break;
        case 'stateUpdate':
            handleStateUpdate(data);
            break;
        case 'heartbeat':
            handleHeartbeat(data);
            break;
    }
}

function handleInitialization(data) {
    isHost = data.isHost;
    document.getElementById('userRole').textContent = isHost ? 'Host' : 'Participant';
    updateControls();
    videoElement.currentTime = data.state.currentTime;
    videoElement.playbackRate = data.state.playbackRate;
    data.state.paused ? videoElement.pause() : videoElement.play();
}

function handleStateUpdate(data) {
    const serverTime = data.timestamp;
    const localTime = Date.now();
    latency = localTime - serverTime;

    const currentTime = videoElement.currentTime;
    const targetTime = data.state.currentTime + (latency / 2000);

    if (Math.abs(currentTime - targetTime) > 0.5) {
        videoElement.currentTime = targetTime;
    }

    if (data.state.paused !== videoElement.paused) {
        data.state.paused ? videoElement.pause() : videoElement.play();
    }

    videoElement.playbackRate = data.state.playbackRate;
}

function handleHeartbeat() {
    // Send heartbeat acknowledgment
    ws.send(JSON.stringify({ type: 'heartbeatAck' }));
}

// Update UI controls
function updateControls() {
    const controls = document.querySelectorAll('.controls button, #seekBar');
    controls.forEach(control => {
        control.disabled = !isHost; // Disable controls for participants
    });

    // Disable native video controls (play/pause) for participants
    videoElement.controls = isHost; // Enable/Disable native controls based on role

    // Disable the native play/pause functionality for participants
    if (!isHost) {
        videoElement.removeAttribute('controls');
    } else {
        videoElement.setAttribute('controls', 'true');
    }
}

// Event listeners for video events
videoElement.addEventListener('play', () => sendPlayerState('play'));
videoElement.addEventListener('pause', () => sendPlayerState('pause'));
videoElement.addEventListener('seeked', () => sendPlayerState('seek'));

videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = formatTime(videoElement.currentTime);
    document.getElementById('seekBar').value = videoElement.currentTime;
});

// Send player state to backend
function sendPlayerState(type) {
    if (!isHost || !ws) return;

    const state = {
        type: 'stateUpdate',
        state: {
            paused: videoElement.paused,
            currentTime: videoElement.currentTime,
            playbackRate: videoElement.playbackRate,
            timestamp: Date.now()
        }
    };

    ws.send(JSON.stringify(state));
}

// Helper functions
function formatTime(seconds) {
    const date = new Date(0);
    date.setSeconds(seconds);
    return date.toISOString().substr(11, 8);
}

function setStatus(message, isError) {
    const statusElement = document.getElementById('statusMessage');
    statusElement.textContent = message;
    statusElement.style.color = isError ? '#D32F2F' : '#388E3C';
    statusElement.style.display = 'block';
    setTimeout(() => {
        statusElement.style.display = 'none';
    }, 3000);
}

// Initialization
document.getElementById('sessionKeyDisplay').textContent = sessionKey;
connectWebSocket();
