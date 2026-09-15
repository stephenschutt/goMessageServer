import SwiftUI
import UIKit

/// A single iMessage-style bubble. Outgoing messages are blue and right-aligned,
/// incoming ones gray and left-aligned, with the tail corner squared off on the
/// side the bubble points from.
///
/// `showsSender` labels incoming bubbles with who wrote them, which a chat of
/// more than two people needs and a chat of two would only clutter.
struct MessageBubble: View {
    let message: Message
    let isMine: Bool
    var showsSender = false

    var body: some View {
        HStack {
            if isMine { Spacer(minLength: 60) }

            VStack(alignment: .leading, spacing: 2) {
                if showsSender && !isMine {
                    Text(message.sender)
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(.secondary)
                }
                Text(message.text)
                    .font(.system(size: 16))
                    .foregroundStyle(isMine ? .white : Color.primary)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(isMine ? Color.bubbleBlue : Color.bubbleGray)
            .clipShape(BubbleShape(pointingRight: isMine))
            .textSelection(.enabled)

            if !isMine { Spacer(minLength: 60) }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 1)
    }
}

/// Rounded rect with one squared-off bottom corner, matching Messages' tail.
private struct BubbleShape: Shape {
    let pointingRight: Bool

    func path(in rect: CGRect) -> Path {
        Path(
            UIBezierPath(
                roundedRect: rect,
                byRoundingCorners: pointingRight
                    ? [.topLeft, .topRight, .bottomLeft]
                    : [.topLeft, .topRight, .bottomRight],
                cornerRadii: CGSize(width: 18, height: 18)
            ).cgPath
        )
    }
}

/// A centered day/time separator, shown when a gap opens up between messages.
struct TimeSeparator: View {
    let date: Date

    var body: some View {
        Text(Self.label(for: date))
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity)
            .padding(.top, 12)
            .padding(.bottom, 4)
    }

    private static func label(for date: Date) -> String {
        let time = date.formatted(date: .omitted, time: .shortened)
        if Calendar.current.isDateInToday(date) {
            return time
        }
        if Calendar.current.isDateInYesterday(date) {
            return "Yesterday  \(time)"
        }
        return "\(date.formatted(date: .abbreviated, time: .omitted))  \(time)"
    }
}

extension Color {
    static let bubbleBlue = Color(red: 0.04, green: 0.52, blue: 1.0)
    static let bubbleGray = Color(UIColor.secondarySystemFill)
}
