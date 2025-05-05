const BACKEND_URL = 'http://localhost:8080';
const STREAMING_URL = ""
let isHost = false;
let ws = null;
let latency = 0;

const videoElement = document.getElementById('videoPlayer');
const urlParams = new URLSearchParams(window.location.search);
const sessionKey = urlParams.get('sessionKey');

let chunkDuration = 0;
let chunkCount = 0;
let videoDuration = 0;

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
        sessionStorage.setItem('hostToken', data.hostToken);

        const newUrl = new URL(window.location.href);
        newUrl.searchParams.set('sessionKey', data.sessionKey);
        newUrl.searchParams.set('hostToken', data.hostToken);
        window.history.replaceState({}, '', newUrl);

        await initializeSession();
    } catch (error) {
        console.error('Failed to create session:', error);
        setStatus('Failed to create session: ' + error.message, true);
    }
}

// Join session
async function initializeSession() {
    try {
        console.log('Initializing session with key:', sessionKey);

        const urlHostToken = urlParams.get('hostToken');
        const storedHostToken = sessionStorage.getItem('hostToken');
        const hostToken = urlHostToken || storedHostToken;

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

        isHost = data.isHost;
        console.log('User role:', isHost ? 'Host' : 'Participant');

        if (isHost && hostToken && !urlHostToken) {
            const newUrl = new URL(window.location.href);
            newUrl.searchParams.set('hostToken', hostToken);
            window.history.replaceState({}, '', newUrl);
        }

        // After receiving streaming URL
        const streamingUrl = data.streaming_url;
        console.log(streamingUrl)
        connectWebSocket(streamingUrl);

        document.getElementById('sessionKeyDisplay').textContent = sessionKey;
        document.getElementById('userRole').textContent = isHost ? 'Host' : 'Participant';
        updateControls();
        attachCustomControlListeners();

        videoElement.addEventListener('error', (e) => {
            console.error('Video error:', videoElement.error);
            setStatus(`Video error: ${videoElement.error.message}`, true);
        });

        videoElement.addEventListener('stalled', () => {
            setStatus('Video stream stalled - reconnecting...', true);
            videoElement.load(); // Attempt to reload
        });
        
        videoElement.addEventListener('loadeddata', () => {
            setStatus('Video stream connected', false);
            if (isHost) {
                videoElement.play().catch(e => {
                    console.log('Autoplay blocked - waiting for user interaction');
                });
            }
        });

    } catch (error) {
        console.error('Session initialization error:', error);
        setStatus('Failed to initialize session: ' + error.message, true);
    }
}

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
        // Request video metadata once connection is established
        getVideoMetadata();
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

function handleInitialization(data) {
    isHost = data.isHost;
    document.getElementById('userRole').textContent = isHost ? 'Admin' : 'Participant';
    updateControls();
    attachCustomControlListeners(); // <- Attach buttons only once we know isHost

    videoElement.currentTime = data.state.currentTime;
    videoElement.playbackRate = data.state.playbackRate;
    data.state.paused ? videoElement.pause() : videoElement.play();
}

function handleSyncMessage(data) {
    switch (data.type) {
        case 'init':
            handleInitialization(data);
            break;
        case 'stateUpdate':
            handleStateUpdate(data);
            break;
        case 'videoMetadata':
            handleVideoMetadata(data);
            break;
        case 'heartbeat':
            handleHeartbeat(data);
            break;
    }
}

function handleVideoMetadata(data) {
    console.log('Video metadata:', data.state);
    chunkDuration = data.state.chunkDuration;
    chunkCount = data.state.chunkCount;
    videoDuration = data.state.videoDuration;
    videoFileType = data.state.videoFileType;
    
    // Get streaming URL from the window
    const streamingUrl = ws.url.replace('ws://', 'http://').replace('/ws', '');
    
    // Create MediaSource
    const mediaSource = new MediaSource();
    videoElement.src = URL.createObjectURL(mediaSource);
    
    mediaSource.addEventListener('sourceopen', async () => {
        // Use correct MIME type for MP4 files
        const mimeCodec = 'video/mp4; codecs="avc1.42E01E, mp4a.40.2"';
        
        // Check if the browser supports this MIME type
        if (!MediaSource.isTypeSupported(mimeCodec)) {
            console.error('Browser does not support this MIME type:', mimeCodec);
            setStatus('Your browser does not support this video format', true);
            return;
        }

        try{
            mediaSource.duration = videoDuration;
            console.log("Setting video duration to:", videoDuration);
            triggerLoadedMetadata();
        } catch (e) {
            console.error('Error setting up MediaSource:', e);
            setStatus('Error setting up video player', true);
        }
        
        // try {
        //     const sourceBuffer = mediaSource.addSourceBuffer(mimeCodec);
        //     mediaSource.duration = videoDuration;
            
        //     // Function to load and append chunks
        //     loadInitialChunks(sourceBuffer, streamingUrl);
            
        //     // Set up monitoring for continuous loading
        //     setupChunkBuffering(sourceBuffer, streamingUrl);
        // } catch (e) {
        //     console.error('Error setting up MediaSource:', e);
        //     setStatus('Error setting up video player', true);
        // }
    });
}

// Send player state to backend
function sendPlayerState(type) {
    if (!isHost || !ws || ws.readyState !== WebSocket.OPEN) return;

    const video_state = {
        type: 'stateUpdate',
        state: {
            paused: videoElement.paused,
            currentTime: videoElement.currentTime,
            playbackRate: videoElement.playbackRate,
            timestamp: Date.now()
        }
    };
    ws.send(JSON.stringify(video_state));
}

function handleStateUpdate(data) {
    const serverTime = data.servertime;
    const localTime = Date.now();

    latency = localTime - serverTime;

    const currentTime = videoElement.currentTime;
    const targetTime = data.state.currentTime + (latency / 1000);

    if (Math.abs(currentTime - targetTime) > 0.5) {
        videoElement.currentTime = targetTime;
    }

    if (data.state.paused !== videoElement.paused) {
        data.state.paused ? videoElement.pause() : videoElement.play();
    }

    videoElement.playbackRate = data.state.playbackRate;
}

function handleHeartbeat() {
    ws.send(JSON.stringify({ type: 'heartbeatAck' }));
}

function getVideoMetadata() {
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        // WebSocket not ready, set timeout to retry
        console.log("WebSocket not ready, retrying in 500ms...");
        setTimeout(getVideoMetadata, 500);
        return;
    }
    
    // WebSocket is open, send request
    console.log("Requesting video metadata");
    ws.send(JSON.stringify({ type: 'videoMetadata' }));
}

// Update UI controls
function updateControls() {
    const playPauseBtn = document.getElementById('playPauseBtn');
    const seekBar = document.getElementById('seekBar');
    playPauseBtn.disabled = !isHost;
    seekBar.disabled = !isHost;
}

// Custom button listeners
function attachCustomControlListeners() {
    const playPauseBtn = document.getElementById('playPauseBtn');
    const seekBar = document.getElementById('seekBar');

    playPauseBtn.addEventListener('click', () => {
        if (!isHost) return;
        if (videoElement.paused) {
            videoElement.play();
        } else {
            videoElement.pause();
        }
    });

    seekBar.addEventListener('input', (e) => {
        if (!isHost) return;
        videoElement.currentTime = parseFloat(e.target.value);
    });
}

// Status message display
function setStatus(message, isError) {
    const statusElement = document.getElementById('statusMessage');
    statusElement.textContent = message;
    statusElement.style.color = isError ? '#D32F2F' : '#388E3C';
    statusElement.style.display = 'block';
    setTimeout(() => {
        statusElement.style.display = 'none';
    }, 3000);
}

// Handle DOM video events
videoElement.addEventListener('play', () => sendPlayerState('play'));
videoElement.addEventListener('pause', () => sendPlayerState('pause'));
videoElement.addEventListener('seeked', () => sendPlayerState('seek'));

videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = formatTime(videoElement.currentTime);
    document.getElementById('seekBar').value = videoElement.currentTime;
});

videoElement.addEventListener('loadedmetadata', () => {
    document.getElementById('seekBar').max = videoElement.duration;
    document.getElementById('duration').textContent = formatTime(videoElement.duration);
});

// Entry
if (sessionKey) {
    console.log('Session key found, starting initialization');
    initializeSession();
} else {
    console.log('No session key found, creating new session');
    createNewSession();
}

///////////////////////////////// CHUNK BUFFERING //////////////////////////////////
// Load first 3 chunks to buffer
async function loadInitialChunks(sourceBuffer, streamingUrl) {
    for (let i = 1; i <= 3; i++) {
        await fetchAndAppendChunk(i, sourceBuffer, streamingUrl);
    }
    
    // Start playback after initial chunks are loaded
    videoElement.play().catch(e => {
        console.log('Autoplay prevented:', e);
    });
}

// Keep loading chunks to maintain at least 3 chunks ahead
function setupChunkBuffering(sourceBuffer, streamingUrl) {
    videoElement.addEventListener('timeupdate', () => {
        const currentChunk = Math.floor(videoElement.currentTime / chunkDuration) + 1;
        const bufferedChunks = getBufferedChunks();
        
        // Always try to keep 3 chunks ahead
        for (let i = 0; i < 3; i++) {
            const chunkToLoad = currentChunk + i;
            if (chunkToLoad <= chunkCount && !bufferedChunks.includes(chunkToLoad)) {
                fetchAndAppendChunk(chunkToLoad, sourceBuffer, streamingUrl);
            }
        }
    });
}

// Helper to fetch and append a chunk
async function fetchAndAppendChunk(chunkIndex, sourceBuffer, streamingUrl) {
    // Format chunkId with leading zeros (001, 002, etc.)
    const chunkId = String(chunkIndex).padStart(3, '0');
    
    try {
        const response = await fetch(`${streamingUrl}/api/video/${chunkId}`);
        const arrayBuffer = await response.arrayBuffer();
        
        // Wait if the source buffer is updating
        await waitForSourceBuffer(sourceBuffer);
        
        // Append the chunk data
        sourceBuffer.appendBuffer(arrayBuffer);
        console.log(`Appended chunk ${chunkId}`);
        
        return new Promise(resolve => {
            sourceBuffer.addEventListener('updateend', () => resolve(), {once: true});
        });
    } catch (e) {
        console.error(`Error loading chunk ${chunkId}:`, e);
    }
}

function waitForSourceBuffer(sourceBuffer) {
    return new Promise(resolve => {
        if (!sourceBuffer.updating) {
            resolve();
        } else {
            sourceBuffer.addEventListener('updateend', () => resolve(), {once: true});
        }
    });
}

function getBufferedChunks() {
    const bufferedChunks = [];
    for (let i = 0; i < videoElement.buffered.length; i++) {
        const start = videoElement.buffered.start(i);
        const end = videoElement.buffered.end(i);
        
        const startChunk = Math.floor(start / chunkDuration) + 1;
        const endChunk = Math.floor(end / chunkDuration) + 1;
        
        for (let j = startChunk; j <= endChunk; j++) {
            bufferedChunks.push(j);
        }
    }
    return bufferedChunks;
}

///////////////////////////////// Helper functions //////////////////////////////////
function formatTime(seconds) {
    const date = new Date(0);
    date.setSeconds(seconds);
    return date.toISOString().substr(11, 8);
}

function triggerLoadedMetadata() {
    const event = new Event('loadedmetadata');
    videoElement.dispatchEvent(event);
}