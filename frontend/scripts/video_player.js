const BACKEND_URL = 'http://localhost:8080';
let isHost = false;
let ws = null;
let latency = 0;

const videoElement = document.getElementById('videoPlayer');
const urlParams = new URLSearchParams(window.location.search);
const sessionKey = urlParams.get('sessionKey');

// Initialize WebSocket connection
function connectWebSocket(streamingUrl) {
    const wsUrl = new URL(`${streamingUrl.replace('http', 'ws')}/ws`);
    wsUrl.searchParams.set('sessionID', sessionKey);
    if (isHost) {
        wsUrl.searchParams.set('isHost', 'true');
    }

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
        console.log('WebSocket connected');
        setStatus('Connected to session', false);
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        handleSyncMessage(data);
    };

    ws.onclose = () => {
        console.log('WebSocket disconnected');
        setStatus('Connection lost - attempting to reconnect...', true);
        setTimeout(() => connectWebSocket(streamingUrl), 3000);
    };

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        setStatus('Connection error', true);
    };
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
    document.getElementById('userRole').textContent = isHost ? 'Admin' : 'Participant';
    updateControls();
    videoElement.currentTime = data.state.currentTime;
    videoElement.playbackRate = data.state.playbackRate;
    data.state.paused ? videoElement.pause() : videoElement.play();
}

function handleStateUpdate(data) {
    console.log('Received state update:', data);
    const serverTime = data.timestamp;
    const localTime = Date.now();
    latency = localTime - serverTime;

    const currentTime = videoElement.currentTime;
    const targetTime = data.state.currentTime + (latency / 2000);

    if (Math.abs(currentTime - targetTime) > 0.5) {
        console.log('Seeking to:', targetTime);
        videoElement.currentTime = targetTime;
    }

    if (data.state.paused !== videoElement.paused) {
        console.log('Updating play state:', data.state.paused ? 'pause' : 'play');
        data.state.paused ? videoElement.pause() : videoElement.play();
    }

    if (videoElement.playbackRate !== data.state.playbackRate) {
        console.log('Updating playback rate:', data.state.playbackRate);
        videoElement.playbackRate = data.state.playbackRate;
    }
}

function handleHeartbeat() {
    // Send heartbeat acknowledgment
    ws.send(JSON.stringify({ type: 'heartbeatAck' }));
}

// Update UI controls
function updateControls() {
    const controls = document.querySelectorAll('.controls button, #seekBar');
    controls.forEach(control => {
        control.disabled = !isHost;
    });
    console.log('Controls updated - Host status:', isHost);
}

// Event listeners for video controls
videoElement.addEventListener('play', () => {
    console.log('Play event triggered');
    sendPlayerState('play');
});

videoElement.addEventListener('pause', () => {
    console.log('Pause event triggered');
    sendPlayerState('pause');
});

videoElement.addEventListener('seeked', () => {
    console.log('Seek event triggered');
    sendPlayerState('seek');
});

// Send player state to backend
function sendPlayerState(type) {
    if (!isHost || !ws) {
        console.log('Not sending state - isHost:', isHost, 'ws connected:', !!ws);
        return;
    }

    console.log('Sending state update:', type);
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

// Add click handlers for control buttons
document.getElementById('playButton').addEventListener('click', () => {
    if (isHost) {
        videoElement.play();
    }
});

document.getElementById('pauseButton').addEventListener('click', () => {
    if (isHost) {
        videoElement.pause();
    }
});

document.getElementById('seekBar').addEventListener('input', (e) => {
    if (isHost) {
        videoElement.currentTime = e.target.value;
    }
});

// Update seek bar and time display
videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = formatTime(videoElement.currentTime);
    document.getElementById('seekBar').value = videoElement.currentTime;
    document.getElementById('seekBar').max = videoElement.duration;
});

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

// Create new session
async function createNewSession() {
    try {
        console.log('Creating new session...');
        const response = await fetch(`${BACKEND_URL}/api/sessions`, {
            method: 'POST'
        });
        
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        console.log('Session created:', data);
        
        // Store host token in session storage
        sessionStorage.setItem('hostToken', data.hostToken);
        
        // Update URL with both session key and host token
        const newUrl = new URL(window.location.href);
        newUrl.searchParams.set('sessionKey', data.sessionKey);
        newUrl.searchParams.set('hostToken', data.hostToken);
        window.history.replaceState({}, '', newUrl);
        
        // Initialize the session
        await initializeSession();
        
    } catch (error) {
        console.error('Failed to create session:', error);
        setStatus('Failed to create session: ' + error.message, true);
    }
}

// Initialize session
async function initializeSession() {
    try {
        console.log('Initializing session with key:', sessionKey);
        
        // Get host token from URL or session storage
        const urlHostToken = urlParams.get('hostToken');
        const storedHostToken = sessionStorage.getItem('hostToken');
        const hostToken = urlHostToken || storedHostToken;
        
        console.log('Host token from URL:', urlHostToken ? 'present' : 'not present');
        console.log('Host token from storage:', storedHostToken ? 'present' : 'not present');

        // Create URL with session key in path and host token as query parameter
        const validateUrl = `${BACKEND_URL}/api/sessions/${encodeURIComponent(sessionKey)}/validate`;
        const urlWithParams = hostToken ? `${validateUrl}?hostToken=${encodeURIComponent(hostToken)}` : validateUrl;
        
        console.log('Sending validation request to:', urlWithParams);
        
        const response = await fetch(urlWithParams);
        
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        console.log('Session validation response:', data);
        
        if (!data.valid) {
            setStatus('Invalid session key', true);
            return;
        }

        // Update host status
        isHost = data.isHost;
        console.log('User role:', isHost ? 'Host' : 'Participant');

        // If user is host, ensure host token is in URL
        if (isHost && hostToken && !urlHostToken) {
            const newUrl = new URL(window.location.href);
            newUrl.searchParams.set('hostToken', hostToken);
            window.history.replaceState({}, '', newUrl);
        }

        // Connect to streaming server
        console.log('Connecting to streaming server:', data.streaming_url);
        connectWebSocket(data.streaming_url);
        
        // Update session info display
        document.getElementById('sessionKeyDisplay').textContent = sessionKey;
        document.getElementById('userRole').textContent = isHost ? 'Admin' : 'Participant';
        updateControls();
        
    } catch (error) {
        console.error('Session initialization error:', error);
        setStatus('Failed to initialize session: ' + error.message, true);
    }
}

// Start initialization if session key is present, otherwise create new session
if (sessionKey) {
    console.log('Session key found, starting initialization');
    initializeSession();
} else {
    console.log('No session key found, creating new session');
    createNewSession();
}