import SwiftUI
import UIKit

/// The conversation screen: Messages-style header naming everyone in the chat,
/// a scrolling transcript, and the compose bar.
///
/// The chat itself is built on the People screen — a username first, then the
/// people to talk to — so every dead end here (no name yet, nobody added yet)
/// leads there.
struct ChatView: View {
    @EnvironmentObject private var client: ChatClient
    @Environment(\.scenePhase) private var scenePhase

    @State private var draft = ""
    @State private var sending = false
    @State private var showingSettings = false
    @State private var showingPeople = false
    @FocusState private var composeFocused: Bool

    /// Messages get a time separator when more than this much quiet passes,
    /// the way Messages breaks up a conversation into sessions.
    private static let separatorGap: TimeInterval = 15 * 60

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                switch client.status {
                case .failed(let reason):
                    banner(reason)
                case .unauthorized:
                    banner("This device isn't authorized yet")
                case .connecting, .connected:
                    if client.needsUsername {
                        banner("Choose a username to start", action: "People") { showingPeople = true }
                    } else if client.chatMembers.isEmpty {
                        banner("No one in this chat yet", action: "Add people") { showingPeople = true }
                    }
                }
                transcript
                Divider()
                composeBar
            }
            .background(Color(UIColor.systemBackground))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) { header }
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        showingPeople = true
                    } label: {
                        Image(systemName: "person.2")
                    }
                    .accessibilityLabel("Your username and the people in this chat")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingSettings = true
                    } label: {
                        Image(systemName: "person.crop.circle.badge.questionmark")
                    }
                    .accessibilityLabel("Identity and server settings")
                }
            }
            .sheet(isPresented: $showingSettings) {
                SettingsView().environmentObject(client)
            }
            .sheet(isPresented: $showingPeople) {
                PeopleView().environmentObject(client)
            }
        }
        .onAppear { client.start() }
        .onChange(of: scenePhase) { _, phase in
            // Stop polling in the background; iOS would suspend the requests
            // anyway, and a stalled request just surfaces as a spurious error.
            if phase == .active { client.start() } else { client.stop() }
        }
    }

    // MARK: - Header

    private var header: some View {
        VStack(spacing: 2) {
            ZStack {
                Circle().fill(Color(UIColor.systemGray3))
                switch client.chatMembers.count {
                case 0:
                    icon("person.fill")
                case 1:
                    Text(client.chatMembers[0].prefix(1).uppercased())
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(.white)
                default:
                    icon("person.2.fill")
                }
            }
            .frame(width: 30, height: 30)

            Text(client.chatTitle.isEmpty ? "No one in this chat" : client.chatTitle)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    private func icon(_ name: String) -> some View {
        Image(systemName: name)
            .font(.system(size: 13, weight: .semibold))
            .foregroundStyle(.white)
    }

    private func banner(_ reason: String, action: String = "Settings",
                        run: (() -> Void)? = nil) -> some View {
        HStack(spacing: 6) {
            Image(systemName: "exclamationmark.triangle.fill")
            Text(reason)
            Spacer()
            Button(action) {
                if let run { run() } else { showingSettings = true }
            }
            .font(.system(size: 12, weight: .semibold))
        }
        .font(.system(size: 12))
        .foregroundStyle(.orange)
        .padding(.horizontal, 14)
        .padding(.vertical, 6)
        .frame(maxWidth: .infinity)
        .background(Color.orange.opacity(0.12))
    }

    // MARK: - Transcript

    private var transcript: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(client.messages.enumerated()), id: \.element.id) { index, message in
                        if needsSeparator(at: index) {
                            TimeSeparator(date: message.timestamp)
                        }
                        MessageBubble(message: message,
                                      isMine: message.sender == client.username,
                                      showsSender: client.chatMembers.count > 1)
                            .id(message.id)
                    }
                    // Anchor for "scroll to the very bottom", which is past the
                    // last bubble once padding is taken into account.
                    Color.clear.frame(height: 1).id(Self.bottomAnchor)
                }
                .padding(.vertical, 8)
            }
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: client.messages.count) { _, _ in
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                }
            }
            .onChange(of: composeFocused) { _, focused in
                if focused { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
            }
        }
    }

    private static let bottomAnchor = "bottom"

    private func needsSeparator(at index: Int) -> Bool {
        guard index > 0 else { return true }
        let gap = client.messages[index].timestamp
            .timeIntervalSince(client.messages[index - 1].timestamp)
        return gap > Self.separatorGap
    }

    // MARK: - Compose

    private var composeBar: some View {
        HStack(alignment: .bottom, spacing: 8) {
            TextField("iMessage", text: $draft, axis: .vertical)
                .lineLimit(1...5)
                .font(.system(size: 16))
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(
                    RoundedRectangle(cornerRadius: 18)
                        .stroke(Color(UIColor.systemGray4), lineWidth: 1)
                )
                .focused($composeFocused)

            Button(action: submit) {
                Image(systemName: "arrow.up")
                    .font(.system(size: 16, weight: .bold))
                    .foregroundStyle(.white)
                    .frame(width: 32, height: 32)
                    .background(canSend ? Color.bubbleBlue : Color(UIColor.systemGray3))
                    .clipShape(Circle())
            }
            .disabled(!canSend)
            .accessibilityLabel("Send")
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(Color(UIColor.systemBackground))
    }

    private var canSend: Bool {
        !sending && !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !client.username.isEmpty
    }

    private func submit() {
        let text = draft
        draft = ""
        sending = true
        Task {
            await client.send(text)
            sending = false
        }
    }
}
